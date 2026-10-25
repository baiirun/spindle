package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// What the hooks put in front of the model. Both tools cap injected context:
// Claude Code at 10,000 characters, Codex at about 2,500 tokens unless the
// handler raises additionalContextLimit. Budgets stay under the smaller.
const (
	StartBudget  = 9_000
	RecallBudget = 3_000
	recallHits   = 5
	recallUsers  = 2
)

const toolsLine = "Search before you rely on memory for an old decision: `memory_search` (decision record and the user's past messages), " +
	"`transcript_search` / `transcript_read` (full transcripts), `memory_write` to record a decision the user made."

// SessionStart is the context for a session that starts, resumes, clears or
// has just compacted. It carries the Now summary and pointers; on a fresh
// start it also carries the user's latest messages from earlier sessions, since
// the new session has none of its own. After a compaction the tool's own
// summary already keeps this session's messages, so they are left out.
func SessionStart(p Project, source, session string, budget int) (string, error) {
	if budget <= 0 {
		budget = StartBudget
	}
	now, err := p.Now()
	if err != nil {
		return "", err
	}
	rec, err := p.LoadRecord()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(now) == "" && len(rec.Entries) == 0 {
		return "", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Project memory (spindle): %s\n\n", p.Name())
	fmt.Fprintf(&b, "The decision record holds %d entries and is not shown here. %s\n\n", len(rec.Entries), toolsLine)
	if strings.TrimSpace(now) != "" {
		b.WriteString("# Now (where the work stood when memory was last written)\n\n")
		b.WriteString(clip(stripNowHeader(now), budget*2/3))
		b.WriteString("\n\n")
	}
	left := budget - b.Len()
	if source != "compact" && left > 500 {
		users, err := p.UserMessages()
		if err != nil {
			return "", err
		}
		if block := recentUsers(users, session, left*2/3); block != "" {
			b.WriteString(block)
			left = budget - b.Len()
		}
	}
	if left > 300 && len(rec.Entries) > 0 {
		b.WriteString(recordIndex(rec, left-50))
	}
	return clip(b.String(), budget), nil
}

var nowHeader = regexp.MustCompile(`^_Written [^\n]*_\n+`)

// stripNowHeader keeps the "written at" line short: the transcript path stays,
// the rest is noise for the model.
func stripNowHeader(now string) string {
	now = strings.TrimSpace(now)
	if m := nowHeader.FindString(now); m != "" {
		return strings.TrimSpace(m) + "\n\n" + strings.TrimSpace(now[len(m):])
	}
	return now
}

// recentUsers renders the newest user messages from other sessions, oldest
// first, that fit in n characters.
func recentUsers(ms []UserMessage, skipSession string, n int) string {
	var lines []string
	size := 0
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if m.Session == skipSession {
			continue
		}
		line := fmt.Sprintf("[%s %s] %s\n", m.ID, m.Time.UTC().Format("2006-01-02 15:04Z"), clip(m.Text, 1200))
		if size+len(line) > n {
			break
		}
		lines = append(lines, line)
		size += len(line)
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# The user's latest messages from earlier sessions (verbatim, %d of %d; search for older ones)\n\n", len(lines), len(ms))
	for i := len(lines) - 1; i >= 0; i-- {
		b.WriteString(lines[i])
	}
	b.WriteString("\n")
	return b.String()
}

// recordIndex lists the most recently changed record keys, so the model knows
// what it can search for.
func recordIndex(r Record, n int) string {
	var b strings.Builder
	b.WriteString("# Decision record index (most recently changed first; search a key for the full entry)\n\n")
	for _, k := range r.Recent() {
		e := r.Entries[k]
		line := fmt.Sprintf("- %s: %s (%s)\n", e.Kind, k, e.Date)
		if b.Len()+len(line) > n {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// recallState is kept per session, in its own file so the prompt hook never
// contends with the worker for the project lock.
type recallState struct {
	Injected map[string]bool `json:"injected"`
	Last     string          `json:"last,omitempty"`
	At       time.Time       `json:"at"`
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (p Project) recallPath(session string) string {
	return filepath.Join(p.Dir, "recall", unsafeID.ReplaceAllString(session, "_")+".json")
}

// Recall returns the context to add to one user prompt: record entries and
// earlier user messages that match it, each shown once per session. The search
// is local and normally takes milliseconds; if it misses the deadline, the
// previous turn's block is reused rather than holding up the prompt.
func Recall(ctx context.Context, p Project, session, prompt string, budget int, deadline time.Duration) (string, error) {
	if budget <= 0 {
		budget = RecallBudget
	}
	st := recallState{Injected: map[string]bool{}}
	if session != "" {
		if err := readJSON(p.recallPath(session), &st); err != nil {
			return "", err
		}
		if st.Injected == nil {
			st.Injected = map[string]bool{}
		}
	}
	type result struct {
		text string
		keys []string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, keys, err := recallBlock(p, session, prompt, st.Injected, budget)
		done <- result{text, keys, err}
	}()
	var r result
	if deadline <= 0 {
		deadline = 1500 * time.Millisecond
	}
	select {
	case r = <-done:
	case <-time.After(deadline):
		return st.Last, nil
	case <-ctx.Done():
		return st.Last, nil
	}
	if r.err != nil || r.text == "" || session == "" {
		return r.text, r.err
	}
	for _, k := range r.keys {
		st.Injected[k] = true
	}
	st.Last, st.At = r.text, time.Now().UTC()
	return r.text, writeJSON(p.recallPath(session), st)
}

func recallBlock(p Project, session, prompt string, seen map[string]bool, budget int) (string, []string, error) {
	if len(Terms(prompt)) == 0 {
		return "", nil, nil
	}
	rec, err := p.LoadRecord()
	if err != nil {
		return "", nil, err
	}
	users, err := p.UserMessages()
	if err != nil {
		return "", nil, err
	}
	var lines, keys []string
	size := 0
	add := func(key, line string) bool {
		if seen[key] || size+len(line) > budget-200 {
			return false
		}
		lines, keys = append(lines, line), append(keys, key)
		size += len(line)
		return true
	}
	n := 0
	for _, h := range rec.Search(prompt, recallHits*3) {
		if n >= recallHits {
			break
		}
		if !relevant(h.Matched, h.Rare) {
			continue
		}
		if add("record:"+h.Key, "- "+RenderEntry(h.Key, h.Entry)+"\n") {
			n++
		}
	}
	n = 0
	for _, m := range SearchUserMessages(users, prompt, session, recallUsers*3, true) {
		if n >= recallUsers {
			break
		}
		line := fmt.Sprintf("- user said [%s %s]: %s\n", m.ID, m.Time.UTC().Format("2006-01-02"), clip(m.Text, 600))
		if add("user:"+m.ID, line) {
			n++
		}
	}
	if len(lines) == 0 {
		return "", nil, nil
	}
	return "Possibly relevant from project memory (spindle). Check before relying on it; `memory_search` for more.\n" + strings.Join(lines, ""), keys, nil
}

// relevant drops weak matches: one common word shared with the prompt is not
// a reason to put an entry in front of the model.
func relevant(matched int, rare bool) bool { return matched >= 2 || rare }
