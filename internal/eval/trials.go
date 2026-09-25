package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

// Trial is one historical continuation moment: at AskedAt the user resumed or
// handed off earlier work. A fresh agent gets Task and (for resume trials) the
// Prior handles, sees only data before AskedAt, and is graded on Checklist.
type Trial struct {
	ID        string    `json:"id"`
	Mode      string    `json:"mode"` // resume: prior handle known | wake: must discover context
	Source    string    `json:"source"`
	Session   string    `json:"session"`
	ItemID    string    `json:"item_id"`
	AskedAt   time.Time `json:"asked_at"`
	Cwd       string    `json:"cwd"`
	Message   string    `json:"message"`   // the user's words
	Task      string    `json:"task"`      // standalone instruction for a fresh agent
	Prior     []string  `json:"prior"`     // source:session handles holding the context
	Checklist []string  `json:"checklist"` // what a good continuation must recover
	// ChecklistFrom maps each item to its 1-based source item when a checklist was split.
	ChecklistFrom []int `json:"checklist_from,omitempty"`
	// ChecklistImportance (must | nice) and ChecklistCites (pre-cutoff item IDs) are set by verification.
	ChecklistImportance []string   `json:"checklist_importance,omitempty"`
	ChecklistCites      [][]string `json:"checklist_cites,omitempty"`
	Pitfalls            []string   `json:"pitfalls"` // mistakes the real agent made and the user corrected
	Confidence          string     `json:"confidence"`
	Notes               string     `json:"notes,omitempty"`
}

var resumeCue = regexp.MustCompile(`(?i)\b(continue|continuing|pick(ing)? (it |this |back )?up|where (were|are) we|left off|take over|took over|other (agent|session|thread|claude|codex)|lost (the|my|this) session|catch up|resume|previous (session|thread|conversation|agent)|last (session|time)|yesterday|original task|back to|transcript|stopped partway|where it (is|was)|where things (are|stand))\b`)

// ResumeCandidate is a user message that may resume earlier work.
type ResumeCandidate struct {
	ItemID  string    `json:"item_id"`
	Source  string    `json:"source"`
	Session string    `json:"session"`
	Cwd     string    `json:"cwd"`
	AskedAt time.Time `json:"asked_at"`
	Text    string    `json:"text"`
	First   bool      `json:"first"`         // first user message of its session
	Why     string    `json:"why,omitempty"` // wide mining: cue | new_session | after_gap
}

// MineResumeCandidates prefilters user messages that may resume earlier work.
func MineResumeCandidates(corpusRoot string) ([]ResumeCandidate, error) {
	paths, err := corpus.ChunkPaths(corpusRoot)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	seenUser := map[string]bool{}
	var out []ResumeCandidate
	for _, p := range paths {
		c, err := corpus.ReadChunk(p)
		if err != nil {
			return nil, err
		}
		for _, it := range c.Items {
			if it.Role != corpus.RoleUser {
				continue
			}
			key := c.Source + ":" + c.Session
			first := !seenUser[key]
			seenUser[key] = true
			if len(it.Text) > 2000 || !resumeCue.MatchString(it.Text) {
				continue
			}
			out = append(out, ResumeCandidate{ItemID: it.ID, Source: c.Source, Session: c.Session, Cwd: c.Cwd, AskedAt: it.Time, Text: it.Text, First: first})
		}
	}
	return out, nil
}

const ResumeClassifySystem = `You classify messages a user sent to an AI coding/design agent.
Keep a message only if the user is RESUMING or HANDING OFF earlier work whose context lives in an earlier
session or earlier in a long session: e.g. "continue from where you left off", "where were we", "look at
the other session and take over", "the app lost the session", "what was the original task before X".
Do not keep: ordinary follow-ups within the current exchange, "do it again", "try again", new tasks,
or questions about external things.`

const resumeClassifySchema = `{"type":"object","properties":{"results":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"},"keep":{"type":"boolean"},"reason":{"type":"string"}},"required":["n","keep","reason"]}}},"required":["results"]}`

// ClassifyResumeCandidates keeps messages that resume or hand off earlier work.
func ClassifyResumeCandidates(ctx context.Context, system string, cands []ResumeCandidate, workers int) ([]ResumeCandidate, error) {
	const batch = 25
	var (
		mu   sync.Mutex
		kept []ResumeCandidate
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, workers)
	)
	for start := 0; start < len(cands); start += batch {
		end := min(start+batch, len(cands))
		wg.Add(1)
		sem <- struct{}{}
		go func(start, end int) {
			defer wg.Done()
			defer func() { <-sem }()
			var b strings.Builder
			for i := start; i < end; i++ {
				why := ""
				if cands[i].Why != "" {
					why = fmt.Sprintf(" why=%q", cands[i].Why)
				}
				fmt.Fprintf(&b, "<message n=%d first_in_session=%t%s>\n%s\n</message>\n", i-start, cands[i].First, why, strings.TrimSpace(cands[i].Text))
			}
			var out struct {
				Results []struct {
					N    int  `json:"n"`
					Keep bool `json:"keep"`
				} `json:"results"`
			}
			_, err := llm.JSON(ctx, llm.Classifier, system, b.String(), resumeClassifySchema, &out)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for _, r := range out.Results {
				if i := start + r.N; r.Keep && i >= start && i < end {
					kept = append(kept, cands[i])
				}
			}
		}(start, end)
	}
	wg.Wait()
	sort.Slice(kept, func(i, j int) bool { return kept[i].AskedAt.Before(kept[j].AskedAt) })
	if len(errs) > 0 {
		return kept, fmt.Errorf("%d classify batches failed; first: %w", len(errs), errs[0])
	}
	return kept, nil
}

const trialLabelSystem = `You build a continuation trial from a real moment where a user resumed or handed off earlier work
to an AI agent. You see: candidate prior sessions (same working directory, before the moment), a few items
before the message, the message, and what happened after (the agent's catch-up and the user's corrections).

Decide what a fresh agent, starting cold at that moment, would need to recover to continue correctly.

The cutoff is strict. The checklist and pitfalls may only contain things that already existed BEFORE the
message: established in the prior sessions or earlier in this session. The "after" section is evidence
for verifying what was true at the moment (what the real agent recovered, what the user confirmed or
corrected about the prior state). Anything newly decided, researched, built, or changed after the message
is off-limits: a fresh agent at the cutoff could not know it.
- keep=false if this isn't really a resumption, or you can't tell what the right continuation needed.
- prior: the handles (exactly as given, "source:session") whose history holds the needed context. Use the
  current session's own handle when the context is earlier in the same session. Empty if unknown.
- mode: "resume" if the user's message or situation identifies the prior session (the harness could pass
  the handle); "wake" if the agent would have to discover the context by searching.
- task: a standalone instruction for the fresh agent, faithful to the user's intent. Don't include answers.
- checklist: 3-6 short, checkable things a good continuation must know or do: the goal, current state,
  key decisions and constraints, what's done, what's next. Ground each in the "after" evidence (what the
  agent recovered and the user accepted, or what the user corrected).
- pitfalls: mistakes the real agent made about the PRIOR state that the user corrected, if any. Not
  disagreements about new work done after the message.
- confidence: high only if the checklist is clearly established by the evidence.`

const trialLabelSchema = `{"type":"object","properties":{"keep":{"type":"boolean"},"mode":{"type":"string","enum":["resume","wake"]},"prior":{"type":"array","items":{"type":"string"}},"task":{"type":"string"},"checklist":{"type":"array","items":{"type":"string"}},"pitfalls":{"type":"array","items":{"type":"string"}},"confidence":{"type":"string","enum":["high","medium","low"]},"notes":{"type":"string"}},"required":["keep","mode","prior","task","checklist","pitfalls","confidence","notes"]}`

type trialLabel struct {
	Keep       bool     `json:"keep"`
	Mode       string   `json:"mode"`
	Prior      []string `json:"prior"`
	Task       string   `json:"task"`
	Checklist  []string `json:"checklist"`
	Pitfalls   []string `json:"pitfalls"`
	Confidence string   `json:"confidence"`
	Notes      string   `json:"notes"`
}

// LabelTrials drafts trials, caching each verdict so interrupted runs resume.
func LabelTrials(ctx context.Context, corpusRoot, cacheDir string, cands []ResumeCandidate, workers int) ([]Trial, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	idx, err := loadChunkIndex(corpusRoot)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu     sync.Mutex
		trials []Trial
		errs   []error
		wg     sync.WaitGroup
		sem    = make(chan struct{}, workers)
	)
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c ResumeCandidate) {
			defer wg.Done()
			defer func() { <-sem }()
			cp := filepath.Join(cacheDir, strings.NewReplacer(":", "_", "#", "_").Replace(c.ItemID)+".json")
			var out trialLabel
			if b, err := os.ReadFile(cp); err != nil || json.Unmarshal(b, &out) != nil {
				prompt, shortlist, err := trialPrompt(corpusRoot, idx, c)
				if err == nil {
					_, err = llm.JSONRequest(ctx, llm.Request{Model: llm.Labeler, Effort: "medium", System: trialLabelSystem, Prompt: prompt, Schema: trialLabelSchema}, &out)
				}
				if err == nil {
					out.Prior = onlyKnown(out.Prior, shortlist)
					b, _ := json.Marshal(out)
					err = os.WriteFile(cp, b, 0o644)
				}
				if err != nil {
					mu.Lock()
					defer mu.Unlock()
					if errors.Is(err, llm.ErrUsageLimit) {
						cancel()
					}
					errs = append(errs, fmt.Errorf("%s: %w", c.ItemID, err))
					return
				}
			}
			if !out.Keep || len(out.Checklist) == 0 {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			trials = append(trials, Trial{
				Mode: out.Mode, Source: c.Source, Session: c.Session, ItemID: c.ItemID, AskedAt: c.AskedAt, Cwd: c.Cwd,
				Message: c.Text, Task: out.Task, Prior: out.Prior, Checklist: out.Checklist, Pitfalls: out.Pitfalls,
				Confidence: out.Confidence, Notes: out.Notes,
			})
		}(c)
	}
	wg.Wait()
	sort.Slice(trials, func(i, j int) bool { return trials[i].AskedAt.Before(trials[j].AskedAt) })
	for i := range trials {
		trials[i].ID = fmt.Sprintf("t-%03d", i+1)
	}
	if len(errs) > 0 {
		return trials, fmt.Errorf("%d label calls failed; first: %w", len(errs), errs[0])
	}
	return trials, nil
}

func onlyKnown(handles, shortlist []string) []string {
	known := map[string]bool{}
	for _, h := range shortlist {
		known[h] = true
	}
	var out []string
	for _, h := range handles {
		if known[strings.TrimSpace(h)] {
			out = append(out, strings.TrimSpace(h))
		}
	}
	return out
}

// trialPrompt shows the labeler the message in context plus a shortlist of
// earlier sessions from the same working directory.
// Labeling windows around the pickup message: enough before to see the state
// at that moment, and only the real agent's catch-up after it.
const (
	labelBeforeBudget = 30000 // characters of the current session before the message
	labelAfterItems   = 12
	labelAfterSpan    = 45 * time.Minute
)

func trialPrompt(corpusRoot string, idx *chunkIndex, c ResumeCandidate) (string, []string, error) {
	type sessionInfo struct {
		handle       string
		start, end   time.Time
		first, last  string
		firstFetched bool
	}
	current := c.Source + ":" + c.Session
	sessions := map[string]*sessionInfo{}
	for _, h := range idx.chunks {
		if h.Cwd != c.Cwd || !h.Start.Before(c.AskedAt) {
			continue
		}
		key := h.Source + ":" + h.Session
		s := sessions[key]
		if s == nil {
			s = &sessionInfo{handle: key, start: h.Start}
			sessions[key] = s
		}
		if h.End.After(s.end) {
			s.end = h.End
		}
	}
	var list []*sessionInfo
	for _, s := range sessions {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].end.After(list[j].end) })
	if len(list) > 10 {
		list = list[:10]
	}
	var shortlist []string
	var b strings.Builder
	b.WriteString("<prior_sessions>\n")
	for _, s := range list {
		source, session, _ := strings.Cut(s.handle, ":")
		chunks, err := corpus.ReadSession(corpusRoot, source, session)
		if err != nil {
			return "", nil, err
		}
		var users []string
		for _, ch := range chunks {
			for _, it := range ch.Items {
				if it.Role == corpus.RoleUser && it.Time.Before(c.AskedAt) {
					users = append(users, clip(strings.Join(strings.Fields(it.Text), " "), 200))
				}
			}
		}
		if len(users) == 0 {
			continue
		}
		shortlist = append(shortlist, s.handle)
		marker := ""
		if s.handle == current {
			marker = " (current session)"
		}
		fmt.Fprintf(&b, "- %s%s %s → %s\n  first user message: %s\n  last user message before the moment: %s\n",
			s.handle, marker, s.start.Format("2006-01-02 15:04"), s.end.Format("2006-01-02 15:04"), users[0], users[len(users)-1])
	}
	b.WriteString("</prior_sessions>\n")

	chunks, err := corpus.ReadSession(corpusRoot, c.Source, c.Session)
	if err != nil {
		return "", nil, err
	}
	var all []corpus.Item
	for _, ch := range chunks {
		for _, it := range ch.Items {
			if it.Role != corpus.RoleTool {
				all = append(all, it)
			}
		}
	}
	at := -1
	for i, it := range all {
		if it.ID == c.ItemID {
			at = i
			break
		}
	}
	if at < 0 {
		return "", nil, fmt.Errorf("%s not found", c.ItemID)
	}
	// The labeler must see the state at the moment, not just its last few turns: v1
	// showed 6 items before and 30 after, and its checklists drifted to later events.
	start, used := at, 0
	for start > 0 && used < labelBeforeBudget {
		start--
		used += len(clip(all[start].Text, 1200)) + 40
	}
	b.WriteString("<before>\n")
	for _, it := range all[start:at] {
		fmt.Fprintf(&b, "[%s %s] %s\n", it.Time.Format("2006-01-02 15:04"), it.Role, clip(it.Text, 1200))
	}
	fmt.Fprintf(&b, "</before>\n<message session=%q asked_at=%q>\n%s\n</message>\n<after>\n", current, all[at].Time.Format("2006-01-02 15:04"), all[at].Text)
	for _, it := range all[at+1 : min(len(all), at+1+labelAfterItems)] {
		if it.Time.Sub(all[at].Time) > labelAfterSpan {
			break
		}
		fmt.Fprintf(&b, "[%s %s] %s\n", it.Time.Format("2006-01-02 15:04"), it.Role, clip(it.Text, 2500))
	}
	b.WriteString("</after>\n")
	return b.String(), shortlist, nil
}

// WriteTrials and ReadTrials persist a trial set.
func WriteTrials(path string, t []Trial) error { return writeJSONL(path, t) }
func ReadTrials(path string) ([]Trial, error)  { return readJSONL[Trial](path) }
func WriteResumeCandidates(path string, c []ResumeCandidate) error {
	return writeJSONL(path, c)
}
func ReadResumeCandidates(path string) ([]ResumeCandidate, error) {
	return readJSONL[ResumeCandidate](path)
}
