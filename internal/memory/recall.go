package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"spindle/internal/model"
)

// Recall follows Claude Code's: on each real user message a side model sees
// the newest memory files as "[type] file (mtime): description" lines and
// picks up to 5. Claude Code runs it alongside the main model and attaches
// the picks after the next tool call, dropping them if the turn ends first.
// Hooks can do the same: UserPromptSubmit starts the selector in the
// background and waits a few seconds, and if it finishes in time the picks
// go in with the prompt; otherwise PostToolUse attaches them after the next
// tool call, and Stop drops them.

const (
	SelectCandidates = 200    // newest files the selector sees
	SelectMax        = 5      // files per message
	SessionBytes     = 61_440 // recalled bytes per session before recall stops (reset by compaction)
)

// Selection is one message's pending pick.
type Selection struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Started   time.Time `json:"started"`
	Done      bool      `json:"done"`
	Files     []string  `json:"files,omitempty"`
	Err       string    `json:"err,omitempty"`
	Delivered bool      `json:"delivered"`
}

// RecallState is one session's recall bookkeeping.
type RecallState struct {
	Surfaced map[string]bool `json:"surfaced"` // files already shown this session
	Bytes    int             `json:"bytes"`
	Pending  *Selection      `json:"pending,omitempty"`
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (p Project) recallPath(session string) string {
	return filepath.Join(p.Dir, "recall", unsafeID.ReplaceAllString(session, "_")+".json")
}

// withRecall reads, changes and writes a session's recall state under its own lock.
func (p Project) withRecall(session string, f func(*RecallState) error) error {
	path := p.recallPath(session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(path+".lock", true)
	if err != nil {
		return err
	}
	defer unlock()
	var st RecallState
	if err := readJSON(path, &st); err != nil {
		return err
	}
	if st.Surfaced == nil {
		st.Surfaced = map[string]bool{}
	}
	if err := f(&st); err != nil {
		return err
	}
	return writeJSON(path, st)
}

// StartRecall records a pending selection for a user message and returns
// its ID, or "" when recall is skipped: a one-word prompt, no memories, or the
// session's recall budget spent.
func (p Project) StartRecall(session, prompt string, now time.Time) (string, error) {
	if len(strings.Fields(prompt)) < 2 {
		return "", nil
	}
	mems, err := p.Load()
	if err != nil || len(mems) == 0 {
		return "", err
	}
	sum := sha256.Sum256([]byte(prompt + now.String()))
	id := hex.EncodeToString(sum[:6])
	skip := false
	err = p.withRecall(session, func(st *RecallState) error {
		if st.Bytes >= SessionBytes {
			skip = true
			return nil
		}
		st.Pending = &Selection{ID: id, Prompt: prompt, Started: now.UTC()}
		return nil
	})
	if skip || err != nil {
		return "", err
	}
	return id, nil
}

// RunSelect runs the selector for a pending selection and stores its picks.
// With local set it uses a word-overlap match instead of a model.
func (p Project) RunSelect(ctx context.Context, session, id string, m model.Model, local bool) error {
	var sel *Selection
	var surfaced []string
	p.withRecall(session, func(st *RecallState) error {
		if st.Pending != nil && st.Pending.ID == id {
			c := *st.Pending
			sel = &c
		}
		for f := range st.Surfaced {
			surfaced = append(surfaced, f)
		}
		return nil
	})
	if sel == nil {
		return nil // superseded by a newer message
	}
	mems, err := p.Load()
	if err != nil {
		return err
	}
	var files []string
	if local {
		files = localSelect(mems, sel.Prompt, surfaced)
	} else {
		files, err = modelSelect(ctx, m, mems, sel.Prompt, surfaced)
	}
	return p.withRecall(session, func(st *RecallState) error {
		if st.Pending == nil || st.Pending.ID != id {
			return nil
		}
		st.Pending.Done, st.Pending.Files = true, files
		if err != nil {
			st.Pending.Err = err.Error() // failures return nothing, as in Claude Code
		}
		return nil
	})
}

func modelSelect(ctx context.Context, m model.Model, mems []Memory, prompt string, surfaced []string) ([]string, error) {
	sort.Strings(surfaced)
	msg := "Available memories:\n" + fileList(mems, SelectCandidates)
	if len(surfaced) > 0 {
		msg += "\nAlready returned for earlier queries in this conversation: " + strings.Join(surfaced, ", ") + "\n"
	}
	msg += "\nSelect memories relevant to:\n" + prompt
	var out struct {
		Selected []string `json:"selected_memories"`
	}
	if err := completeJSON(ctx, m, selectSystem, msg, selectSchema, &out); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for i, mem := range mems {
		if i < SelectCandidates {
			known[mem.File()] = true
		}
	}
	var files []string
	for _, f := range out.Selected {
		f = NameOf(f) + ".md"
		if known[f] && !contains(files, f) && len(files) < SelectMax {
			files = append(files, f)
		}
	}
	return files, nil
}

// localSelect matches prompt words against names and descriptions: Claude
// Code's local-index path, for when no model should be called.
func localSelect(mems []Memory, prompt string, surfaced []string) []string {
	q := words(prompt)
	type hit struct {
		file  string
		score int
	}
	var hits []hit
	for _, m := range mems {
		if contains(surfaced, m.File()) {
			continue
		}
		have := words(strings.ReplaceAll(m.Name, "-", " ") + " " + m.Description)
		n := 0
		for w := range q {
			if match(w, have) {
				n++
			}
		}
		if n >= 2 { // one shared word is surface overlap, not relevance
			hits = append(hits, hit{m.File(), n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var files []string
	for i := 0; i < len(hits) && i < SelectMax; i++ {
		files = append(files, hits[i].file)
	}
	return files
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("the a an and or of to in on for with is are was were be been it its this that these those as at by from we you i our your did do does what which who why how when about any not no yes into than then there their they them has have had but if so can will would should could just also me my let ok okay go keep going please now next continue use using") {
		stop[w] = true
	}
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) > 2 && !stop[w] {
			out[stem(w)] = true
		}
	}
	return out
}

// stem strips a common suffix so "stored", "stores" and "store" meet.
func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s", "e"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// match reports whether w, or a word it is a 4+ letter prefix of, is in set.
func match(w string, set map[string]bool) bool {
	if set[w] {
		return true
	}
	if len(w) < 4 {
		return false
	}
	for h := range set {
		if strings.HasPrefix(h, w) || (len(h) >= 4 && strings.HasPrefix(w, h)) {
			return true
		}
	}
	return false
}

// WaitRecall waits up to d for the pending selection to finish and returns
// its attachment, or "" if it is not done (PostToolUse will try again).
func (p Project) WaitRecall(session, id string, d time.Duration) (string, error) {
	deadline := time.Now().Add(d)
	for {
		text, done, err := p.attach(session, id)
		if err != nil || done || time.Now().After(deadline) {
			return text, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// AttachPending returns the pending selection's attachment if it finished
// and was not delivered yet: the PostToolUse path.
func (p Project) AttachPending(session string) (string, error) {
	text, _, err := p.attach(session, "")
	return text, err
}

func (p Project) attach(session, id string) (string, bool, error) {
	var text string
	done := false
	err := p.withRecall(session, func(st *RecallState) error {
		sel := st.Pending
		if sel == nil || (id != "" && sel.ID != id) {
			done = true
			return nil
		}
		if !sel.Done || sel.Delivered {
			done = sel.Delivered
			return nil
		}
		done, sel.Delivered = true, true
		var parts []string
		for _, f := range sel.Files {
			if st.Surfaced[f] {
				continue
			}
			m, ok := p.Get(f)
			if !ok {
				continue
			}
			parts = append(parts, recalled(m, time.Now()))
			st.Surfaced[f] = true
		}
		if len(parts) == 0 {
			return nil
		}
		text = RecallPrefix + "\n\n" + strings.Join(parts, "\n\n")
		st.Bytes += len(text)
		return nil
	})
	return text, done, err
}

// recalled renders one attached memory, with Claude Code's age warning for
// memories older than a day.
func recalled(m Memory, now time.Time) string {
	var b strings.Builder
	if days := int(now.Sub(m.Modified).Hours() / 24); days >= 1 {
		fmt.Fprintf(&b, "This memory is %d days old. Memories are point-in-time observations, not live state — claims about code behavior or file:line citations may be outdated. Verify against current code before asserting as fact.\n\n", days)
	}
	body := m.Render()
	if len(body) > FileBytes {
		body = clipRunes(body, FileBytes) + "\n(cut; memory_read for the rest)"
	}
	fmt.Fprintf(&b, "Memory: %s:\n\n%s", m.File(), body)
	return b.String()
}

// DropPending discards a selection that didn't arrive before the turn ended.
func (p Project) DropPending(session string) error {
	if _, err := os.Stat(p.recallPath(session)); err != nil {
		return nil
	}
	return p.withRecall(session, func(st *RecallState) error { st.Pending = nil; return nil })
}

// ResetRecall forgets what the session was shown: after a compaction the
// attachments are gone from context, so they may be shown again.
func (p Project) ResetRecall(session string) error {
	if _, err := os.Stat(p.recallPath(session)); err != nil {
		return nil
	}
	return p.withRecall(session, func(st *RecallState) error {
		*st = RecallState{Surfaced: map[string]bool{}}
		return nil
	})
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Search ranks memories by how many query words their name, description and
// body share, then by recency. It backs the memory_search tool.
func Search(mems []Memory, query string, limit int) []Memory {
	if limit <= 0 {
		limit = 10
	}
	q := words(query)
	type hit struct {
		m     Memory
		score int
	}
	var hits []hit
	for _, m := range mems {
		head := words(strings.ReplaceAll(m.Name, "-", " ") + " " + m.Description)
		body := words(m.Body)
		n := 0
		for w := range q {
			if match(w, head) {
				n += 2
			} else if match(w, body) {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, hit{m, n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score }) // Load order is newest first
	var out []Memory
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].m)
	}
	return out
}
