package memory

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"spindle/internal/model"
	"spindle/internal/transcript"
)

// Ingest reads a session's new messages and has a side model write zero or
// more gists about them, each pointing at the message it came from. It
// replaces v1's extraction: it decides nothing about rules or the wiki, it
// only notes what happened. Dream sorts the notes later.

// Cursor is how far ingest has read one session.
type Cursor struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Line    int       `json:"line"`
	At      time.Time `json:"at"` // last pass
}

// Cursors reads how far ingest has read each session, by its location.
func Cursors(home string) (map[string]*Cursor, error) {
	c := map[string]*Cursor{}
	err := readJSON(filepath.Join(home, "cursors.json"), &c)
	return c, err
}

func updateCursors(home string, f func(map[string]*Cursor)) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(home, "cursors.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := Cursors(home)
	if err != nil {
		return err
	}
	f(c)
	return writeJSON(filepath.Join(home, "cursors.json"), c)
}

// Ingest thresholds.
const (
	minUserChars = 20     // new user text below this skips the pass ("go", "ok")
	ingestWindow = 60_000 // characters of new messages sent; older ones in a long gap are cut
	messageChars = 6_000  // one message is cut at this many characters
)

// Scope is where the side model says a gist belongs.
type Scope string

const (
	ScopeProject Scope = "project" // about this project's work
	ScopeGlobal  Scope = "global"  // about the user, or true in every project
)

// IngestResult says what a pass did.
type IngestResult struct {
	Skip    string // why no model call ran: "no new messages", "too little user text", "subagent"
	Project int    // entries added to the session's project
	Global  int    // entries added to global
	Unfiled int    // project-scoped gists put in unfiled because the folder has no project
	Through int    // the cursor after the pass
}

// Ingest runs one pass over a session's messages after its cursor. p is the
// session's context: its project, which may be none.
//
// The start is the later of the cursor and the last line any entry of this
// session cites in the stores ingest writes to: p's (or unfiled) and global. So a pass that appended and then
// crashed before moving the cursor isn't read twice. The model also sees the
// newest entries of both stores, so it doesn't repeat what the agent already
// appended with memory_append. The reply is
// {"gists":[{"gist":"...","line":N,"scope":"project"|"global"}]}.
//
// Without a project, project-scoped gists go to the unfiled store, with the
// session's folder, until that folder is linked (see Adopt).
//
// Ensures: on a model error or a reply that isn't the schema, nothing is
// appended and the cursor stays, so the next pass retries.
// Ensures: a gist citing a line outside the new messages, or failing
// ParseGist, is dropped and counted in the activity log, not fatal.
// Ensures: the cursor moves to the last new message only after the entries
// are appended.
func Ingest(ctx context.Context, p Project, s transcript.Session, m model.Model, now time.Time) (IngestResult, error) {
	panic("unimplemented")
}
