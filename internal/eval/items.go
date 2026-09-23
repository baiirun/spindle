// Package eval builds decision eval sets from real recall questions and
// scores memory designs against them. See docs/eval.md.
package eval

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// Candidate is a user message that may be asking to recall a past decision.
type Candidate struct {
	ItemID   string    `json:"item_id"`
	Source   string    `json:"source"`
	Session  string    `json:"session"`
	Cwd      string    `json:"cwd"`
	AskedAt  time.Time `json:"asked_at"`
	Text     string    `json:"text"`
	Kind     string    `json:"kind,omitempty"`
	Classify string    `json:"classify_reason,omitempty"`
}

// Item is one labeled eval question.
type Item struct {
	ID          string    `json:"id"`
	Question    string    `json:"question"` // standalone rewrite, answerable by a fresh agent
	Original    string    `json:"original"` // the user's words
	ItemID      string    `json:"item_id"`  // transcript item where it was asked
	Source      string    `json:"source"`
	Session     string    `json:"session"`
	Cwd         string    `json:"cwd"`
	AskedAt     time.Time `json:"asked_at"`
	Kind        string    `json:"kind"` // current | who | why | changed | rejected
	Gold        []string  `json:"gold"`
	Attribution string    `json:"attribution,omitempty"` // who proposed it and the user's stance, when known
	Confidence  string    `json:"confidence"`            // high | medium | low
	Notes       string    `json:"notes,omitempty"`
}

// Kinds are the decision question kinds scored in v0.
var Kinds = []string{"current", "who", "why", "changed", "rejected"}

func writeJSONL[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return w.Flush()
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r T
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, sc.Err()
}

// ReadItems loads a labeled eval set.
func ReadItems(path string) ([]Item, error) { return readJSONL[Item](path) }

// WriteCandidates and ReadCandidates persist classified candidates.
func WriteCandidates(path string, c []Candidate) error { return writeJSONL(path, c) }
func ReadCandidates(path string) ([]Candidate, error)  { return readJSONL[Candidate](path) }

// WriteItems persists a labeled eval set.
func WriteItems(path string, items []Item) error { return writeJSONL(path, items) }
