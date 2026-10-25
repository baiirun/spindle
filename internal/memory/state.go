package memory

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// Cursor is how far extraction has read one transcript.
type Cursor struct {
	Agent     string    `json:"agent"`
	Session   string    `json:"session"`
	Line      int       `json:"line"`      // last transcript line folded into the record
	Pending   int       `json:"pending"`   // conversation characters seen past Line, not yet folded
	SeenAt    time.Time `json:"seen_at"`   // last Stop for this transcript
	FoldedAt  time.Time `json:"folded_at"` // last extraction
	Extracted int       `json:"extracted"` // extraction passes
}

// State is everything the project keeps besides the record and the Now summary.
type State struct {
	Cursors map[string]*Cursor `json:"cursors"` // by transcript path
	// Dream gate.
	LastDream     time.Time       `json:"last_dream"`
	SessionsSince map[string]bool `json:"sessions_since_dream"`
}

func (p Project) LoadState() (State, error) {
	s := State{}
	if err := readJSON(p.path("state.json"), &s); err != nil {
		return s, err
	}
	if s.Cursors == nil {
		s.Cursors = map[string]*Cursor{}
	}
	if s.SessionsSince == nil {
		s.SessionsSince = map[string]bool{}
	}
	return s, nil
}

// SaveState writes the state. Only the worker writes it, holding the project lock.
func (p Project) SaveState(s State) error {
	return writeJSON(p.path("state.json"), s)
}

func (p Project) LoadRecord() (Record, error) {
	r := Record{}
	if err := readJSON(p.path("record.json"), &r); err != nil {
		return r, err
	}
	if r.Entries == nil {
		r.Entries = map[string]Entry{}
	}
	return r, nil
}

func (p Project) SaveRecord(r Record) error { return writeJSON(p.path("record.json"), r) }

// Now returns the current Now summary, or "" if none was written yet.
func (p Project) Now() (string, error) {
	b, err := os.ReadFile(p.path("now.md"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func (p Project) saveNow(text string) error { return writeFile(p.path("now.md"), []byte(text)) }

// UserMessage is one verbatim user message.
type UserMessage struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Text    string    `json:"text"`
}

// UserMessages reads the project's user-message log, oldest first.
func (p Project) UserMessages() ([]UserMessage, error) {
	f, err := os.Open(p.path("users.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []UserMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m UserMessage
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Text != "" {
			out = append(out, m)
		}
	}
	return out, sc.Err()
}

// appendUserMessages adds messages to the log. Callers hold the project lock.
func (p Project) appendUserMessages(ms []UserMessage) error {
	if len(ms) == 0 {
		return nil
	}
	f, err := os.OpenFile(p.path("users.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, m := range ms {
		line, err := json.Marshal(m)
		if err != nil {
			f.Close()
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SearchUserMessages ranks the user's past messages against a query, newest
// first among equal scores. skipSession leaves out one session's messages;
// strict keeps only matches that pass the recall relevance bar.
func SearchUserMessages(ms []UserMessage, query, skipSession string, limit int, strict bool) []UserMessage {
	docs := map[string]string{}
	byID := map[string]UserMessage{}
	for _, m := range ms {
		if skipSession != "" && m.Session == skipSession {
			continue
		}
		docs[m.ID] = m.Text
		byID[m.ID] = m
	}
	var out []UserMessage
	for _, s := range rank(docs, query) {
		if strict && !relevant(s.matched, s.rare) {
			continue
		}
		out = append(out, byID[s.key])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
