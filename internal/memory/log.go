package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EntryID names one log entry, "e-..." and never reused. NewEntryID makes
// them time-ordered; MigrateV1 derives them from what it migrates, so a rerun
// can tell what it already wrote. Nothing else reads meaning into them.
type EntryID string

// SourceKind is where an entry came from.
type SourceKind string

const (
	FromClaude   SourceKind = "claude_session"
	FromCodex    SourceKind = "codex_session"
	FromOpenCode SourceKind = "opencode_session"
	FromAgent    SourceKind = "agent"      // memory_append, mid-session
	FromCLI      SourceKind = "cli"        // spin log append
	FromV1       SourceKind = "spindle_v1" // a v1 memory file or change, by MigrateV1
)

// Pointer is where to check an entry against what was actually said. Path
// is a transcript or file; Line is the transcript line of the message the
// gist came from, 0 when the source has no lines. Note is free text for
// sources that are neither ("the Nov 8 planning call").
type Pointer struct {
	Kind SourceKind `json:"kind"`
	Path string     `json:"path,omitempty"`
	Line int        `json:"line,omitempty"`
	Note string     `json:"note,omitempty"`
}

// GistBytes caps a gist, after OptMem: one line a reader takes in at a glance.
const GistBytes = 280

// Gist is one non-empty line of at most GistBytes bytes. Only ParseGist makes one.
type Gist string

// ParseGist collapses runs of whitespace (newlines included) to one space and
// trims. It returns an error for an empty result or one over GistBytes; it
// never clips, so a gist is never silently cut mid-thought.
func ParseGist(s string) (Gist, error) {
	g := oneLine(s)
	if g == "" {
		return "", errors.New("empty gist")
	}
	if len(g) > GistBytes {
		return "", fmt.Errorf("gist is %d bytes, over %d: say it in one shorter line", len(g), GistBytes)
	}
	return Gist(g), nil
}

// Entry is one line of a store's log. The store is the file it is in, so it
// isn't repeated here.
type Entry struct {
	ID      EntryID   `json:"id"`
	At      time.Time `json:"at"`
	Session string    `json:"session,omitempty"` // the session it came from; "" for entries outside one
	Folder  string    `json:"folder,omitempty"`  // the session's folder, when known
	Source  Pointer   `json:"source"`
	Gist    Gist      `json:"gist"`
	// Adopted is the unfiled entry this one copies, set only by Adopt. The
	// copy keeps the original's time, session, folder and source.
	Adopted EntryID `json:"adopted,omitempty"`
}

// NewEntryID returns a fresh id. Ids sort by the millisecond they were made
// in; within one millisecond their order is random.
func NewEntryID(now time.Time) EntryID {
	var r [4]byte
	_, _ = rand.Read(r[:])
	return EntryID(fmt.Sprintf("e-%011x%s", now.UnixMilli(), hex.EncodeToString(r[:])))
}

func (p Project) logPath() string { return filepath.Join(p.Dir, "log.jsonl") }

// Append adds entries to the end of the store's log, in order, creating the
// store if needed.
//
// Requires: every entry has an ID, a time and a parsed Gist.
// Ensures: all entries are written in one write under the store's lock, so
// concurrent appends interleave whole batches and a crash tears at most the
// final line.
// Must not: rewrite or reorder existing lines.
func (p Project) Append(entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	for i, e := range entries {
		if e.ID == "" || e.At.IsZero() || e.Gist == "" {
			return fmt.Errorf("append to %s: entry %d has no id, time or gist", p.Name, i)
		}
	}
	if err := p.Init(); err != nil {
		return err
	}
	unlock, err := p.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return appendJSONL(p.logPath(), entries)
}

// Entries reads the store's log, oldest first. A missing log is empty.
//
// Ensures: a torn final line (a crash mid-append) is ignored.
// Ensures: any other line that doesn't parse is an error naming the store and
// line number: the log is the source of truth, so damage is surfaced, not skipped.
func (p Project) Entries() ([]Entry, error) {
	if p.Name == "" {
		return nil, nil
	}
	b, err := os.ReadFile(p.logPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	lines = lines[:len(lines)-1] // after the last newline: "" or a torn line
	out := make([]Entry, 0, len(lines))
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s/log.jsonl line %d: %w", p.Name, i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}
