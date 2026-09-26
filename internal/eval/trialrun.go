package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"spindle/internal/llm"
	"spindle/internal/snapshot"
)

// Trial arms. Both see the same transcripts before the cutoff; only the
// episodes arm also sees episode projections.
const (
	ArmRaw      = "raw"
	ArmEpisodes = "episodes"
	ArmNative   = "native" // resume the real Codex thread, cut at the trial's moment
	ArmCold     = "cold"   // no history at all: the floor any memory has to beat
)

// TrialRunOptions configures one scored pass over a trial set.
type TrialRunOptions struct {
	CorpusRoot  string
	EpisodeRoot string
	Version     string // episode version directory name
	Binary      string // built spin binary
	Arm         string
	Trials      []Trial
	RunDir      string
	ScratchDir  string // snapshots live here; removed per trial
	Workers     int
}

// TrialResult is one trial's outcome in one arm.
type TrialResult struct {
	Trial        string         `json:"trial"`
	Arm          string         `json:"arm"`
	Mode         string         `json:"mode"`
	Brief        string         `json:"brief"`
	Grade        TrialGrade     `json:"grade"`
	Coverage     float64        `json:"coverage"` // covered = 1, partial = 0.5, over checklist items
	SpinCalls    int            `json:"spin_calls"`
	OtherCalls   []string       `json:"other_calls,omitempty"` // non-spin commands: possible leakage, audited by hand
	UsedResume   bool           `json:"used_resume"`
	FoundPrior   bool           `json:"found_prior"` // a spin command referenced a prior session (discovery trials)
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	DurationMS   int64          `json:"duration_ms"`
	ToolCalls    []llm.ToolCall `json:"tool_calls,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// TrialGrade is the judge's verdict.
type TrialGrade struct {
	Checklist []string `json:"checklist"` // covered | partial | missing | contradicted, per item
	Pitfalls  []string `json:"pitfalls"`  // avoided | repeated | unclear, per pitfall
	Reason    string   `json:"reason"`
}

const trialJudgeSystem = `You grade a fresh agent's continuation brief against a checklist of what a good continuation
of this work had to recover. For each checklist item: "covered" if the brief states it (meaning, not wording),
"partial" if it's there but incomplete or vague, "contradicted" if the brief states something incompatible,
otherwise "missing". For each known pitfall (a mistake the real agent made and the user corrected):
"avoided" if the brief clearly doesn't make it, "repeated" if it does, "unclear" otherwise.`

const trialJudgeSchema = `{"type":"object","properties":{"checklist":{"type":"array","items":{"type":"string","enum":["covered","partial","missing","contradicted"]}},"pitfalls":{"type":"array","items":{"type":"string","enum":["avoided","repeated","unclear"]}},"reason":{"type":"string"}},"required":["checklist","pitfalls","reason"]}`

// RunTrials scores every trial in one arm.
func RunTrials(ctx context.Context, o TrialRunOptions) ([]TrialResult, error) {
	if err := os.MkdirAll(o.RunDir, 0o755); err != nil {
		return nil, err
	}
	var (
		mu      sync.Mutex
		results []TrialResult
		wg      sync.WaitGroup
		sem     = make(chan struct{}, max(1, o.Workers))
	)
	for _, t := range o.Trials {
		wg.Add(1)
		sem <- struct{}{}
		go func(t Trial) {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := runTrial(ctx, o, t)
			if err != nil {
				r.Error = err.Error()
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Trial < results[j].Trial })
	return results, writeJSONL(filepath.Join(o.RunDir, "results.jsonl"), results)
}

func runTrial(ctx context.Context, o TrialRunOptions, t Trial) (TrialResult, error) {
	r := TrialResult{Trial: t.ID, Arm: o.Arm, Mode: t.Mode}
	var err error
	switch o.Arm {
	case ArmNative:
		err = nativeBrief(ctx, o, t, &r)
	case ArmCold:
		err = coldBrief(ctx, o, t, &r)
	default:
		err = spinBrief(ctx, o, t, &r)
	}
	if err != nil {
		return r, err
	}

	var items strings.Builder
	for i, c := range t.Checklist {
		fmt.Fprintf(&items, "%d. %s\n", i+1, c)
	}
	var pits strings.Builder
	for i, p := range t.Pitfalls {
		fmt.Fprintf(&pits, "%d. %s\n", i+1, p)
	}
	prompt := fmt.Sprintf("<task>%s</task>\n<checklist>\n%s</checklist>\n<pitfalls>\n%s</pitfalls>\n<brief>\n%s\n</brief>", t.Task, items.String(), pits.String(), r.Brief)
	if _, err := llm.JSONRequest(ctx, llm.Request{Model: llm.Judge, Effort: "medium", System: trialJudgeSystem, Prompt: prompt, Schema: trialJudgeSchema}, &r.Grade); err != nil {
		return r, fmt.Errorf("judge: %w", err)
	}
	score := 0.0
	for _, g := range r.Grade.Checklist {
		switch g {
		case "covered":
			score++
		case "partial":
			score += 0.5
		}
	}
	if len(t.Checklist) > 0 {
		r.Coverage = score / float64(len(t.Checklist))
	}
	return r, nil
}

func trialSystem(t Trial, bin, corpusRoot, episodes string) string {
	q := shellQuote
	roots := fmt.Sprintf("--corpus %s --episodes %s", q(corpusRoot), q(episodes))
	var start string
	if t.Mode == "resume" && len(t.Prior) > 0 {
		var cmds []string
		for _, h := range t.Prior {
			source, session, _ := strings.Cut(h, ":")
			cmds = append(cmds, fmt.Sprintf("  %s resume %s --source %s --session %s", q(bin), roots, q(source), q(session)))
		}
		start = "The harness knows which earlier session(s) this work continues. Start by running:\n\n" + strings.Join(cmds, "\n")
	} else {
		start = fmt.Sprintf("This is a brand-new session, started in %s. Nobody has told you which earlier session(s) this\ncontinues; find them yourself. Start with:\n\n  %s wake %s --query \"...\"", t.Cwd, q(bin), roots)
	}
	return fmt.Sprintf(`You are a fresh agent taking over an existing effort. You have no remembered context. Your only job
here is to write the handoff brief you would need to continue the work; you will not do the work itself,
so don't stop to say you can't edit or implement anything.

%s

Other tools, all read-only:
  %s search %s --query "..." [--source S --session ID]   find episodes and transcript passages
  %s read %s REF                                         expand an episode, transcript range, or item ID
  %s related --episodes %s EPISODE_REF                   follow an episode's continuation links

Use only this command. Do not read other files or directories, and do not edit anything.

Write the brief with these headings: Goal, Current state,
Decisions and constraints, Done, Next concrete action, Open questions. Be specific and cite transcript
item IDs where you can.`, start, q(bin), roots, q(bin), roots, q(bin), q(episodes))
}

// TrialSummary aggregates one arm.
type TrialSummary struct {
	Arm          string  `json:"arm"`
	N            int     `json:"n"`
	Errors       int     `json:"errors"`
	Coverage     float64 `json:"coverage"`
	Contradicted float64 `json:"contradicted"` // share of trials with any contradicted item
	PitfallsRep  int     `json:"pitfalls_repeated"`
	SpinCalls    float64 `json:"spin_calls"`
	InputTokens  float64 `json:"input_tokens"`
	DurationS    float64 `json:"duration_s"`
	LeakSuspects int     `json:"leak_suspects"` // trials with non-spin commands
}

// SummarizeTrials computes arm-level metrics.
func SummarizeTrials(arm string, rs []TrialResult) TrialSummary {
	s := TrialSummary{Arm: arm}
	for _, r := range rs {
		if r.Error != "" {
			s.Errors++
			continue
		}
		s.N++
		s.Coverage += r.Coverage
		for _, g := range r.Grade.Checklist {
			if g == "contradicted" {
				s.Contradicted++
				break
			}
		}
		for _, p := range r.Grade.Pitfalls {
			if p == "repeated" {
				s.PitfallsRep++
			}
		}
		s.SpinCalls += float64(r.SpinCalls)
		s.InputTokens += float64(r.InputTokens)
		s.DurationS += float64(r.DurationMS) / 1000
		if len(r.OtherCalls) > 0 {
			s.LeakSuspects++
		}
	}
	if s.N > 0 {
		n := float64(s.N)
		s.Coverage /= n
		s.Contradicted /= n
		s.SpinCalls /= n
		s.InputTokens /= n
		s.DurationS /= n
	}
	return s
}

// spinBrief has a fresh agent write the brief using only spin over a cutoff snapshot.
func spinBrief(ctx context.Context, o TrialRunOptions, t Trial, r *TrialResult) error {
	home := filepath.Join(o.ScratchDir, t.ID)
	defer os.RemoveAll(home)
	episodeRoot := o.EpisodeRoot
	if o.Arm == ArmRaw {
		episodeRoot = ""
	}
	if _, err := snapshot.Build(snapshot.Options{CorpusRoot: o.CorpusRoot, EpisodeRoot: episodeRoot, Out: home, Version: o.Version, Before: t.AskedAt}); err != nil {
		return err
	}
	work := filepath.Join(home, "work") // empty working directory: nothing to read but spin
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	corpusRoot := filepath.Join(home, "corpus")
	episodes := filepath.Join(home, "episodes", o.Version)

	started := time.Now()
	res, err := llm.Run(ctx, llm.Request{
		Model: llm.Reader, Dir: work, Timeout: 10 * time.Minute,
		System: trialSystem(t, o.Binary, corpusRoot, episodes),
		Prompt: trialUserTurn(t),
	})
	r.Brief, r.ToolCalls, r.DurationMS = res.Text, res.ToolCalls, time.Since(started).Milliseconds()
	r.InputTokens, r.OutputTokens = res.InputTokens, res.OutputTokens
	r.UsedResume = usedResume(res.ToolCalls)
	for _, c := range res.ToolCalls {
		var ev commandEvent
		if json.Unmarshal(c.Input, &ev) != nil || ev.Item.Command == "" {
			continue
		}
		if strings.Contains(ev.Item.Command, o.Binary) {
			r.SpinCalls++
			r.FoundPrior = r.FoundPrior || mentionsPrior(t, ev.Item.Command)
		} else {
			r.OtherCalls = append(r.OtherCalls, ev.Item.Command)
		}
	}
	return err
}

// coldBrief has a fresh agent write the brief from the request alone, with no
// history and nothing to read: what a new session knows without memory.
func coldBrief(ctx context.Context, o TrialRunOptions, t Trial, r *TrialResult) error {
	work := filepath.Join(o.ScratchDir, t.ID, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Join(o.ScratchDir, t.ID))
	started := time.Now()
	res, err := llm.Run(ctx, llm.Request{
		Model: llm.Reader, Dir: work, Timeout: 10 * time.Minute,
		System: `You are a fresh agent taking over an existing effort. You have no remembered context and no access
to earlier sessions, files, or tools. Your only job is to write the handoff brief you would need to continue
the work. Say plainly what you can't know. Use these headings: Goal, Current state, Decisions and constraints,
Done, Next concrete action, Open questions.`,
		Prompt: trialUserTurn(t),
	})
	r.Brief, r.ToolCalls, r.DurationMS = res.Text, res.ToolCalls, time.Since(started).Milliseconds()
	r.InputTokens, r.OutputTokens = res.InputTokens, res.OutputTokens
	for _, c := range res.ToolCalls {
		var ev commandEvent
		if json.Unmarshal(c.Input, &ev) == nil && ev.Item.Command != "" {
			r.OtherCalls = append(r.OtherCalls, ev.Item.Command)
		}
	}
	return err
}

// mentionsPrior reports whether text names one of the trial's prior sessions
// by the 8-character prefix that transcript and episode references carry.
func mentionsPrior(t Trial, text string) bool {
	for _, h := range t.Prior {
		if _, session, ok := strings.Cut(h, ":"); ok && len(session) >= 8 && strings.Contains(text, session[:8]) {
			return true
		}
	}
	return false
}

// trialUserTurn is the user turn every arm gets. A wake trial replays a brand-new
// session, so the task is the user's opening message rather than a description.
func trialUserTurn(t Trial) string {
	when := t.AskedAt.Format("2006-01-02 15:04 MST")
	if t.Mode == "wake" {
		return fmt.Sprintf("It is %s. The user opens a new session with:\n\n%s\n\nBefore doing anything, write the handoff brief you'd need to continue this work.", when, t.Task)
	}
	return fmt.Sprintf("It is %s. The work you are picking up:\n\n%s\n\nWrite the handoff brief you'd need to continue it.", when, t.Task)
}
