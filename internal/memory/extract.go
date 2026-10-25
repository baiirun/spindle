package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindle/internal/corpus"
)

// Extraction folds the conversation since the transcript's cursor into the
// project memory: record edits (the ledger half), a fresh Now summary, the new
// user messages verbatim, and the transcript's corpus chunks for search.

// Thresholds. Stop fires after every turn; folding a few lines at a time would
// cost a model call per turn and gives the record nothing to decide on.
const (
	MinChars     = 12_000  // new conversation characters before a Stop triggers extraction
	spanChars    = 300_000 // a larger span is folded in pieces
	nowWindow    = 150_000 // characters of the session the Now summary is written from
	nowBudget    = 6_000
	verbatimChar = 3_000
)

const recordSystem = `You keep the decision record of one long-running project, as the assistant in its conversation. The record is a
long, detailed log that a future assistant searches when it needs an earlier decision, its reason, or whether
something was already tried; it is not shown in the prompt, so it has no size limit. Another summary covers the
current state and next step. You receive the current record and the conversation since it was last updated. Return
the edit operations that bring the record up to date, as JSON. Code applies them.

Entry kinds:
- decision: a decision or rule currently in force. text: what was decided, stated fully enough to apply it without
  the conversation (keep exact values, names, numbers, wording, and the scope it applies to). why: the reason, in
  the user's reasoning where possible. who: "user" (the user stated it), "user approved" (the assistant proposed it
  and the user agreed; put the user's approving words in user_words, verbatim), or "assistant" (an implementation
  choice the assistant made and the user did not object to).
- tried: something tried or rejected, what happened, and why it failed or was turned down. who: who rejected it.
- preference: a standing preference or correction from the user about how to work. Put the user's words in
  user_words.

Operations (fill unused fields with "" or []):
- add: a new entry. key: a short, specific, searchable topic name (the words someone would search for).
- replace: an existing entry changed. Reuse its exact key. Code keeps the old text as "replaced", so state the new
  version fully and say in why what changed and why.
- drop: an entry no longer holds. key, reason. Prefer replace when a decision was changed rather than abandoned, so
  the old version stays visible. A reversed decision can also become a "tried" entry.

Rules:
- Be detailed: one entry per distinct decision, not merged summaries. Old decisions still in force stay, however old.
- Only replace or drop an entry when the conversation actually changed, reversed or abandoned it. Check every
  existing entry against the new conversation; a stale entry is the worst failure. No operation for unchanged
  entries, and never drop an entry to save space.
- Distinguish user decisions from assistant proposals. Do not turn a proposal into a commitment: an assistant
  proposal the user has not accepted is not a decision. Moving on is not agreeing.
- Point to files instead of copying their content: name the file or artifact, don't paste it.
- Cite 1-2 item IDs exactly as they appear in the conversation (codex:<8 hex>#L<line> or claude:<8 hex>#L<line>)
  for every add and replace. Never invent one.

What never goes in the record (the other summary rewrites it every time):
- progress, status, milestones, completed or in-flight work, commits, test results ("M4 is complete", "slices 1-3
  landed", "the sheet was rebuilt");
- open questions, pending approvals, and next steps;
- one-off instructions for a single task ("commit this", "fix 5-8").
A decision is a choice that constrains future work and would still matter weeks later.`

const nowSystem = `You are the assistant in the conversation below, and your context is about to be handed to a future assistant
(after compaction, or in a fresh session on the same project). Write the "Now" half of the continuation summary:
what that assistant needs to pick the work up exactly where it stands. Decisions, rejected ideas and standing
preferences are kept in a separate searchable record; don't restate them. The latest user message and your last
reply are copied verbatim by code, so don't quote them in full.

Sections (Markdown headings, in this order):
## Where the work stands
What is done, what is in flight, and which files or artifacts are involved (name them; don't copy their content).
## Next step
The exact next action, in line with the user's latest request. If the user has not approved it, say it is a proposal.
## Open questions and waiting on
Unanswered questions, pending approvals and their limits, delegated or running work, and what we are waiting on.

Rules:
- End every line with the item ID(s) it comes from, in brackets, exactly as they appear (codex:<8 hex>#L<line> or
  claude:<8 hex>#L<line>).
- Distinguish user decisions from assistant proposals or assumptions. Do not turn proposals into commitments,
  reports into verified results, partial checks into verified correctness, or historical evidence into current proof.
- Stay under %d characters.`

// ReadTranscript parses a Claude Code or Codex transcript, telling them apart
// by content. ok is false for a subagent or empty session.
func ReadTranscript(path, agent string) (corpus.Session, bool, error) {
	if agent == "" {
		agent = DetectAgent(path)
	}
	if agent == "codex" {
		return corpus.ReadCodexSession(path)
	}
	return corpus.ReadClaudeSession(path)
}

// DetectAgent says which tool wrote a transcript: a Codex rollout starts with a
// session_meta line; anything else is read as Claude Code.
func DetectAgent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		if strings.Contains(filepath.ToSlash(path), "/.codex/") {
			return "codex"
		}
		return "claude"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for i := 0; i < 5 && sc.Scan(); i++ {
		var l struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && (l.Type == "session_meta" || l.Type == "turn_context" || l.Type == "response_item") {
			return "codex"
		}
	}
	return "claude"
}

// ExtractOptions configures one extraction pass.
type ExtractOptions struct {
	Model    Model
	Corpus   string // corpus root for transcript chunks; empty skips
	MinChars int    // 0 means MinChars
	Now      func() time.Time
}

// ExtractResult says what a pass did.
type ExtractResult struct {
	Folded   bool // false: below the threshold, or nothing new
	NewChars int
	Users    int
	Record   ApplyResult
}

// Extract folds a transcript's new conversation into the project. Model calls
// run without the project lock; the edits are applied under it.
func Extract(ctx context.Context, p Project, transcript, agent string, force bool, o ExtractOptions) (ExtractResult, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MinChars == 0 {
		o.MinChars = MinChars
	}
	var res ExtractResult
	if err := p.Init(); err != nil {
		return res, err
	}
	s, ok, err := ReadTranscript(transcript, agent)
	if err != nil || !ok {
		return res, err
	}
	state, err := p.LoadState()
	if err != nil {
		return res, err
	}
	cur := state.Cursors[transcript]
	if cur == nil {
		cur = &Cursor{Agent: s.Source, Session: s.ID}
	}
	var fresh []corpus.Item
	for _, it := range s.Items {
		if it.Line > cur.Line && (it.Role == corpus.RoleUser || it.Role == corpus.RoleAssistant) {
			fresh = append(fresh, it)
		}
	}
	for _, it := range fresh {
		res.NewChars += len(it.Render()) + 2
	}
	if len(fresh) == 0 || (!force && res.NewChars < o.MinChars) {
		unlock, err := p.Lock()
		if err != nil {
			return res, err
		}
		defer unlock()
		if state, err = p.LoadState(); err != nil {
			return res, err
		}
		cur = orCursor(state.Cursors[transcript], s)
		cur.Pending, cur.SeenAt = res.NewChars, o.Now().UTC()
		state.Cursors[transcript] = cur
		return res, p.SaveState(state)
	}

	valid := map[string]bool{}
	for _, it := range s.Items {
		valid[it.ID] = true
	}
	record, err := p.LoadRecord()
	if err != nil {
		return res, err
	}
	var ops []Op
	for _, span := range spans(fresh, spanChars) {
		var out struct {
			Ops []Op `json:"ops"`
		}
		prompt := fmt.Sprintf("<record entries=%d>\n%s\n</record>\n\n<conversation_since>\n%s</conversation_since>", len(record.Entries), record.Render(), span)
		if err := o.Model.JSON(ctx, recordSystem, prompt, opsSchema, &out); err != nil {
			return res, fmt.Errorf("record: %w", err)
		}
		// Apply to a scratch copy so the next span sees the edits; the real
		// apply happens under the lock against the record as it is then.
		record.Apply(out.Ops, o.Now(), valid, "extract")
		ops = append(ops, out.Ops...)
	}
	now, err := writeNow(ctx, o.Model, s, o.Now())
	if err != nil {
		return res, fmt.Errorf("now: %w", err)
	}

	var users []UserMessage
	for _, it := range fresh {
		if it.Role == corpus.RoleUser && strings.TrimSpace(it.Text) != "" {
			users = append(users, UserMessage{ID: it.ID, Time: it.Time, Agent: s.Source, Session: s.ID, Text: it.Text})
		}
	}
	res.Users = len(users)

	unlock, err := p.Lock()
	if err != nil {
		return res, err
	}
	defer unlock()
	if record, err = p.LoadRecord(); err != nil {
		return res, err
	}
	res.Record = record.Apply(ops, o.Now(), valid, "extract")
	if err := p.SaveRecord(record); err != nil {
		return res, err
	}
	if err := p.saveNow(now); err != nil {
		return res, err
	}
	if err := p.appendUserMessages(users); err != nil {
		return res, err
	}
	if o.Corpus != "" {
		for _, c := range corpus.SplitChunks(s) {
			if _, err := c.Write(o.Corpus); err != nil {
				return res, err
			}
		}
	}
	if state, err = p.LoadState(); err != nil {
		return res, err
	}
	cur = orCursor(state.Cursors[transcript], s)
	cur.Line = fresh[len(fresh)-1].Line
	cur.Pending = 0
	cur.SeenAt, cur.FoldedAt = o.Now().UTC(), o.Now().UTC()
	cur.Extracted++
	state.Cursors[transcript] = cur
	state.SessionsSince[s.ID] = true
	res.Folded = true
	return res, p.SaveState(state)
}

func orCursor(c *Cursor, s corpus.Session) *Cursor {
	if c == nil {
		return &Cursor{Agent: s.Source, Session: s.ID}
	}
	return c
}

// spans renders items and cuts them into pieces of at most n characters.
func spans(items []corpus.Item, n int) []string {
	var out []string
	var b strings.Builder
	for _, it := range items {
		line := it.Render() + "\n\n"
		if b.Len() > 0 && b.Len()+len(line) > n {
			out = append(out, b.String())
			b.Reset()
		}
		b.WriteString(line)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// writeNow writes the Now summary from the session's last nowWindow
// characters, under a header that points back to the transcript.
func writeNow(ctx context.Context, m Model, s corpus.Session, at time.Time) (string, error) {
	var b strings.Builder
	for _, it := range s.Items {
		if it.Role == corpus.RoleUser || it.Role == corpus.RoleAssistant {
			b.WriteString(it.Render())
			b.WriteString("\n\n")
		}
	}
	window := b.String()
	if len(window) > nowWindow {
		window = window[len(window)-nowWindow:]
		if i := strings.Index(window, "\n\n["); i >= 0 {
			window = window[i+2:]
		}
	}
	prompt := "<conversation>\n" + window + "</conversation>"
	// The model sometimes returns a cut-off summary; retry until all three sections are there.
	var text string
	for try := 0; try < 3; try++ {
		out, err := m.Text(ctx, fmt.Sprintf(nowSystem, nowBudget), prompt)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(out)
		if strings.Contains(text, "## Where the work stands") && strings.Contains(text, "## Next step") && strings.Contains(text, "## Open questions") {
			break
		}
	}
	head := fmt.Sprintf("_Written %s from %s session %s (transcript: %s)._", at.UTC().Format("2006-01-02 15:04Z"), s.Source, s.ID, s.Path)
	return head + "\n\n" + lastExchange(s) + "\n\n" + text + "\n", nil
}

// lastExchange copies the latest user message and the assistant's reply to
// it, verbatim (clipped), so the ask is never paraphrased.
func lastExchange(s corpus.Session) string {
	var user, reply *corpus.Item
	for i := range s.Items {
		it := &s.Items[i]
		switch it.Role {
		case corpus.RoleUser:
			user, reply = it, nil
		case corpus.RoleAssistant:
			if user != nil {
				reply = it
			}
		}
	}
	var b strings.Builder
	b.WriteString("## Latest ask and last exchange (verbatim)\n")
	for _, it := range []*corpus.Item{user, reply} {
		if it != nil {
			fmt.Fprintf(&b, "[%s %s %s]\n%s\n\n", it.ID, it.Time.UTC().Format(time.RFC3339), it.Role, clip(it.Text, verbatimChar))
		}
	}
	return strings.TrimSpace(b.String())
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(clipped)"
}
