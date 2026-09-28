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
	ArmHandoff     = "handoff" // summary + turns since it, without the full user-message history (Factory Droid's shape)
	ArmTail        = "tail"    // only the verbatim turns since the thread's last real compaction
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
	case ArmUserHandoff, ArmHandoff:
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
	if o.Tail || o.Arm == ArmTail {
		tail, since, err := recentTail(s, t)
		if err != nil {
			return err
		}
		extra += fmt.Sprintf("\n\n<recent_turns since=%q>\n%s</recent_turns>\n\nThe recent turns are both sides of the conversation, verbatim, since the thread's last context compaction.", since.UTC().Format(time.RFC3339), tail)
	}
	if o.TwoPart {
		sum, _, err := TwoPartSummary(s, t, o.FreshSummary)
		if err != nil {
			return err
		}
		if sum != "" {
			extra = "\n\n<compaction_summary>\n" + sum + "\n</compaction_summary>" + extra
		}
	} else if o.FreshSummary > 0 {
		sum, err := freshSummary(ctx, s, t, o.FreshSummary)
		if err != nil {
			return err
		}
		extra = "\n\n<compaction_summary>\n" + sum + "\n</compaction_summary>" + extra
	}
	if o.Arm == ArmTail || o.NoUserHistory {
		return contextOnlyBrief(ctx, o, t, r, extra)
	}
	msgs := renderItems(s, time.Time{}, t.AskedAt, corpus.RoleUser)
	which := "all of the user's own messages in\nthis conversation so far"
	if o.Arm == ArmHandoff {
		return handoffOnlyBrief(ctx, o, t, r, extra)
	}
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

// handoffOnlyBrief answers from the compaction summary and the verbatim turns
// since it, as a compacting agent would, with no separate user-message history.
func handoffOnlyBrief(ctx context.Context, o TrialRunOptions, t Trial, r *TrialResult, context string) error {
	work := filepath.Join(o.ScratchDir, t.ID, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Join(o.ScratchDir, t.ID))
	system := fmt.Sprintf("You are continuing a long-running project conversation whose earlier context was compacted. You have the\n"+
		"compaction summary and the verbatim turns since it.\n\nUse only this context. Do not run commands or read files; your only job is to %s.\n%s",
		trialJob(t), finalInstruction(t))
	started := time.Now()
	res, err := llm.Run(ctx, llm.Request{Model: llm.Reader, Dir: work, Timeout: 10 * time.Minute, System: system, Prompt: strings.TrimSpace(context) + "\n\n" + trialUserTurn(t)})
	r.Brief, r.ToolCalls, r.DurationMS = res.Text, res.ToolCalls, time.Since(started).Milliseconds()
	r.InputTokens, r.OutputTokens = res.InputTokens, res.OutputTokens
	return err
}

// recentTail renders both sides of the conversation since the real thread's
// last compaction before the cutoff: what a compacting agent still has verbatim.
func recentTail(s corpus.Session, t Trial) (string, time.Time, error) {
	path, err := findCodexRollout(NativePrior(t))
	if err != nil {
		return "", time.Time{}, err
	}
	cuts, err := codexCompactions(path)
	if err != nil {
		return "", time.Time{}, err
	}
	var since time.Time
	for _, c := range cuts {
		if c.Before(t.AskedAt) && c.After(since) {
			since = c
		}
	}
	return renderItems(s, since, t.AskedAt, corpus.RoleUser, corpus.RoleAssistant), since, nil
}

// freshSummary writes one handoff summary at the thread's last real compaction
// before the cutoff, from the preceding window of both sides of the
// conversation (up to maxChars), with Droid's adaptive prompt. Unlike the
// chained handoffs, it is written fresh, as a compacting agent sees its context.
//
// Summaries are cached per session, compaction and window size under
// freshSummaryCache, so every run of a case reads the same summary and reruns
// measure the reader, not summary regeneration.
func freshSummary(ctx context.Context, s corpus.Session, t Trial, maxChars int) (string, error) {
	_, since, err := recentTail(s, t)
	if err != nil {
		return "", err
	}
	cache := filepath.Join(freshSummaryCache, fmt.Sprintf("%s-%d-%d.md", NativePrior(t), since.Unix(), maxChars))
	if b, err := os.ReadFile(cache); err == nil {
		return string(b), nil
	}
	out, err := writeFreshSummary(ctx, s, since, maxChars)
	if err != nil {
		return "", err
	}
	// Link, not rename: if a concurrent run cached this summary first, keep and
	// use theirs so all runs agree.
	if err := os.MkdirAll(freshSummaryCache, 0o755); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", cache, os.Getpid())
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	_ = os.Link(tmp, cache)
	b, err := os.ReadFile(cache)
	return string(b), err
}

// freshSummaryCache holds cached fresh summaries (gitignored with the rest of data/).
const freshSummaryCache = "data/eval/fresh-summary-cache"

func writeFreshSummary(ctx context.Context, s corpus.Session, since time.Time, maxChars int) (string, error) {
	window := renderItems(s, time.Time{}, since, corpus.RoleUser, corpus.RoleAssistant)
	if len(window) > maxChars {
		window = window[len(window)-maxChars:]
		if i := strings.Index(window, "\n\n["); i >= 0 {
			window = window[i+2:]
		}
	}
	prompt := strings.Replace(droidAdaptivePrompt,
		"You've previously produced a summary of the session up to a certain point. There have been new messages since then. You must update the summary to cover these messages, adhering to the guidelines provided below.",
		"You are to read the full conversation and produce a summary based on guidelines provided below.", 1)
	res, err := llm.Run(ctx, llm.Request{Model: llm.Reader, System: "You are the assistant in the conversation below.\n\n" + prompt,
		Prompt: "<conversation>\n" + window + "</conversation>", Timeout: 15 * time.Minute})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(res.Text)
	if i := strings.Index(out, "<summary>"); i >= 0 {
		out = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out[i+len("<summary>"):]), "</summary>"))
	}
	return out, nil
}

// contextOnlyBrief answers from the assembled context blocks alone, without the
// user-message history.
func contextOnlyBrief(ctx context.Context, o TrialRunOptions, t Trial, r *TrialResult, blocks string) error {
	work := filepath.Join(o.ScratchDir, t.ID, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Join(o.ScratchDir, t.ID))
	system := fmt.Sprintf("You are continuing a long-running project conversation. You have only the context below.\n\n"+
		"Use only this context. Do not run commands or read files; your only job is to %s.\n%s", trialJob(t), finalInstruction(t))
	started := time.Now()
	res, err := llm.Run(ctx, llm.Request{Model: llm.Reader, Dir: work, Timeout: 10 * time.Minute, System: system, Prompt: strings.TrimSpace(blocks) + "\n\n" + trialUserTurn(t)})
	r.Brief, r.ToolCalls, r.DurationMS = res.Text, res.ToolCalls, time.Since(started).Milliseconds()
	r.InputTokens, r.OutputTokens = res.InputTokens, res.OutputTokens
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

// droidAdaptivePrompt is Factory Droid's adaptive compaction prompt (feature
// flag AdaptiveCompactionPrompt in droid 0.228), in its update-a-previous-summary form.
const droidAdaptivePrompt = `Summarize this conversation or agent trajectory so the assistant can continue effectively after compaction.
You've previously produced a summary of the session up to a certain point. There have been new messages since then. You must update the summary to cover these messages, adhering to the guidelines provided below.
Choose the sections and level of detail that fit this session. Organize around what matters for continuation, not a chronological account of tool calls.
Favor a thorough, complete summary. Use your judgment to prioritize the material and allocate detail where it helps most. Do not omit useful context merely to keep the summary short.
Always preserve relevant user context: the primary request and intent, the latest request, consequential requirements, clarifications, preferences, approvals and their limits, corrections, and unanswered questions. Distinguish user decisions from assistant proposals or assumptions.
Preserve other context according to its value for continuing the work. This may include current line of investigation, important discoveries and reasoning, decisions, outstanding delegated work, unresolved findings, and immediate next actions. Keep identifiers and locations needed to act on that context.
Project files, recorded evidence, and other relevant artifacts may already be durable sources of truth. Prefer referencing these sources while preserving important context that exists only in the conversation.
When updating a previous summary, integrate new information, replace superseded state, and remove details that no longer help continuation. Do not turn proposals into commitments, reports into verified results, partial checks into verified correctness, or historical evidence into current proof.
Return the summary inside <summary> tags.`

// SnapshotPrompts are the chained snapshot formats BuildHandoffs can write.
var SnapshotPrompts = map[string]string{"codex": codexCompactPrompt, "claude": claudeCompactPrompt, "state": stateSnapshotPrompt, "droid": droidAdaptivePrompt}

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
		if i := strings.Index(prev, "<summary>"); i >= 0 {
			prev = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(prev[i+len("<summary>"):]), "</summary>"))
		}
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
