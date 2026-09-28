package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/dream"
	"spindle/internal/llm"
)

// User-message arms. Codex compaction keeps every user message verbatim and
// squeezes only the assistant's side into a short encrypted summary. These arms
// separate the two: the user's own words alone, the user's words plus spindle's
// project memory, and the user's words plus a readable stand-in for Codex's
// summary (its public fallback compaction prompt, chained the same way).
const (
	ArmUser        = "user"
	ArmUserMemory  = "user-memory"
	ArmUserHandoff = "user-handoff"
)

// codexCompactPrompt is Codex CLI's local (non-remote) compaction prompt, as
// shipped in codex-cli 0.156. The remote endpoint's prompt is not public.
const codexCompactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue
Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

// priorSession reads the trial's Codex prior from its source rollout.
func priorSession(t Trial) (corpus.Session, error) {
	id := NativePrior(t)
	if id == "" {
		return corpus.Session{}, fmt.Errorf("trial has no Codex prior session")
	}
	path, err := findCodexRollout(id)
	if err != nil {
		return corpus.Session{}, err
	}
	s, _, err := corpus.ReadCodexSession(path)
	return s, err
}

// renderItems renders the session's items with a role in roles and a time in [from, to).
func renderItems(s corpus.Session, from, to time.Time, roles ...corpus.Role) string {
	var b strings.Builder
	for _, it := range s.Items {
		if it.Time.Before(from) || !it.Time.Before(to) {
			continue
		}
		for _, r := range roles {
			if it.Role == r {
				b.WriteString(it.Render())
				b.WriteString("\n\n")
				break
			}
		}
	}
	return b.String()
}

func userBrief(ctx context.Context, o TrialRunOptions, t Trial, r *TrialResult) error {
	s, err := priorSession(t)
	if err != nil {
		return err
	}
	var extra string
	switch o.Arm {
	case ArmUserMemory:
		text, step, err := memorySnapshot(o.MemoryDir, t)
		if err != nil {
			return err
		}
		r.MemoryAsOf = step.AsOf.UTC().Format("2006-01-02T15:04Z")
		if h, err := os.ReadFile(strings.TrimSuffix(step.Path, ".md") + ".history.md"); err == nil {
			text += "\n\n" + string(h)
		}
		extra = fmt.Sprintf("\n\n<project_memory as_of=%q>\n%s\n</project_memory>\n\nThe project memory is a fold of the whole "+
			"conversation (both sides) through its date. Treat it as the authority on current rules and decisions; use the\n"+
			"user's messages for their reasons and for anything after the memory's date.", r.MemoryAsOf, text)
	case ArmUserHandoff:
		text, step, err := memorySnapshot(o.MemoryDir, t)
		if err != nil {
			return err
		}
		r.MemoryAsOf = step.AsOf.UTC().Format("2006-01-02T15:04Z")
		tail := renderItems(s, step.AsOf, t.AskedAt, corpus.RoleUser, corpus.RoleAssistant)
		extra = fmt.Sprintf("\n\n<handoff_summary as_of=%q>\n%s\n</handoff_summary>\n\n<recent_turns>\n%s</recent_turns>\n\n"+
			"The handoff summary was written at the last context compaction, covering both sides of the conversation.\n"+
			"Recent turns (both sides) follow it verbatim.", r.MemoryAsOf, text, tail)
	}
	msgs := renderItems(s, time.Time{}, t.AskedAt, corpus.RoleUser)
	which := "all of the user's own messages in\nthis conversation so far"
	if o.UserLast > 0 {
		msgs = lastUserItems(s, t.AskedAt, o.UserLast)
		which = fmt.Sprintf("the user's most recent %d messages in\nthis conversation (older ones are not included)", o.UserLast)
	}

	work := filepath.Join(o.ScratchDir, t.ID, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Join(o.ScratchDir, t.ID))
	system := fmt.Sprintf("You are continuing a long-running project conversation. Below are %s, verbatim and in order, with item IDs. The assistant's replies are not included.%s\n\n"+
		"Use only this context. Do not run commands or read files; your only job is to %s.\n%s",
		which, map[bool]string{true: " Additional context follows them.", false: ""}[extra != ""], trialJob(t), finalInstruction(t))
	prompt := "<user_messages>\n" + msgs + "</user_messages>" + extra + "\n\n" + trialUserTurn(t)
	started := time.Now()
	res, err := llm.Run(ctx, llm.Request{Model: llm.Reader, Dir: work, Timeout: 10 * time.Minute, System: system, Prompt: prompt})
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

// lastUserItems renders the user's last n messages before the cutoff.
func lastUserItems(s corpus.Session, before time.Time, n int) string {
	var kept []corpus.Item
	for _, it := range s.Items {
		if it.Role == corpus.RoleUser && it.Time.Before(before) {
			kept = append(kept, it)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	var b strings.Builder
	for _, it := range kept {
		b.WriteString(it.Render())
		b.WriteString("\n\n")
	}
	return b.String()
}

// codexCompactions returns the times the real Codex thread compacted.
func codexCompactions(path string) ([]time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		var l struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.Type == "compacted" {
			out = append(out, l.Timestamp)
		}
	}
	return out, sc.Err()
}

// claudeCompactPrompt paraphrases the section structure of Claude Code's
// compaction summary.
const claudeCompactPrompt = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's
explicit requests and your previous actions. It must capture everything needed to continue the work without losing
context. Use these sections:
1. Primary Request and Intent: all of the user's explicit requests and intents, in detail.
2. Key Technical Concepts: important concepts, rules, and designs discussed.
3. Files and Artifacts: files and documents examined, created, or modified, and what they now contain.
4. Errors and Fixes: mistakes made and how they were fixed, especially where the user corrected you.
5. Problem Solving: problems solved and ongoing explorations.
6. All User Messages: every user message that isn't a tool result, condensed but preserving intent and corrections.
7. Pending Tasks: tasks explicitly asked for that are not done.
8. Current Work: precisely what was being worked on most recently.
9. Optional Next Step: the next step in line with the user's most recent requests, quoting the latest exchange.`

// stateSnapshotPrompt is spindle's proposed working-state snapshot.
const stateSnapshotPrompt = `Write the project's working-state snapshot: the state of the work as of now, for a fresh agent who must
answer questions and continue it. Rewrite it completely from the previous snapshot and the conversation since; don't
append a history. Sections:
1. Current rules and decisions: every rule or decision currently in force, with exact values. For each: one line on
   why (in the user's reasoning where possible), who decided (the user, or the agent's proposal the user accepted, and
   with what words), what it replaced if it changed, and the item ID(s) that establish it. When the user answered a
   proposal with a short "yes", "ok let's try it", or similar, state exactly what was accepted.
2. Rejected or dropped: ideas tried and rejected, with the reason and item ID.
3. Open threads and deferred items.
4. Next steps.
Keep separate projects or games in the same conversation clearly separated. Be complete on section 1; be brief elsewhere.`

// SnapshotPrompts are the chained snapshot formats BuildHandoffs can write.
var SnapshotPrompts = map[string]string{"codex": codexCompactPrompt, "claude": claudeCompactPrompt, "state": stateSnapshotPrompt}

// BuildHandoffs writes a chained summary at each of the real thread's
// compaction points plus any extra cuts: summary n folds summary n-1 plus the
// conversation (user and assistant turns) since the previous point. Output
// uses the dream step layout so memory arms can read it.
func BuildHandoffs(ctx context.Context, session, outDir, format string, extra []time.Time) ([]dream.Step, error) {
	instruction, ok := SnapshotPrompts[format]
	if !ok {
		return nil, fmt.Errorf("unknown snapshot format %q", format)
	}
	path, err := findCodexRollout(session)
	if err != nil {
		return nil, err
	}
	s, _, err := corpus.ReadCodexSession(path)
	if err != nil {
		return nil, err
	}
	cuts, err := codexCompactions(path)
	if err != nil {
		return nil, err
	}
	cuts = append(cuts, extra...)
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Before(cuts[j]) })
	if err := os.MkdirAll(filepath.Join(outDir, "steps"), 0o755); err != nil {
		return nil, err
	}
	var steps []dream.Step
	var prev string
	var from time.Time
	for i, cut := range cuts {
		conv := renderItems(s, from, cut, corpus.RoleUser, corpus.RoleAssistant)
		prompt := "<previous_summary>\n" + prev + "\n</previous_summary>\n\n<conversation_since>\n" + conv + "</conversation_since>"
		system := "You are the assistant in the conversation below. The previous summary is your own handoff from the\n" +
			"last compaction; the conversation since then follows it.\n\n" + instruction
		res, err := llm.Run(ctx, llm.Request{Model: llm.Reader, System: system, Prompt: prompt, Timeout: 15 * time.Minute})
		if err != nil {
			return steps, fmt.Errorf("handoff %d: %w", i+1, err)
		}
		prev = strings.TrimSpace(res.Text)
		p := filepath.Join(outDir, "steps", fmt.Sprintf("%03d.md", i+1))
		if err := os.WriteFile(p, []byte(prev+"\n"), 0o644); err != nil {
			return steps, err
		}
		steps = append(steps, dream.Step{N: i + 1, AsOf: cut, Path: p, Chars: len(prev)})
		b, _ := json.MarshalIndent(steps, "", "  ")
		if err := os.WriteFile(filepath.Join(outDir, "steps.json"), b, 0o644); err != nil {
			return steps, err
		}
		fmt.Printf("handoff %d/%d as of %s: %d chars from %d chars of conversation\n", i+1, len(cuts), cut.Format(time.RFC3339), len(prev), len(conv))
		from = cut
	}
	return steps, nil
}
