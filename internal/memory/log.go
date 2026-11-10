package memory

import (
	"path/filepath"
	"time"
)

// EntryID names one log entry: "e-" then a time-ordered, random suffix. Ids
// are never reused, and nothing derives meaning from their characters.
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
	panic("unimplemented")
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

// NewEntryID returns a fresh id that sorts after every id made earlier on
// this machine's clock.
func NewEntryID(now time.Time) EntryID {
	panic("unimplemented")
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
	panic("unimplemented")
}

// Entries reads the store's log, oldest first. A missing log is empty.
//
// Ensures: a torn final line (a crash mid-append) is ignored.
// Ensures: any other line that doesn't parse is an error naming the store and
// line number: the log is the source of truth, so damage is surfaced, not skipped.
func (p Project) Entries() ([]Entry, error) {
	panic("unimplemented")
}
