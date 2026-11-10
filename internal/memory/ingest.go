package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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
	var res IngestResult
	if s.Subagent {
		res.Skip = "subagent"
		return res, nil
	}
	global := Store(p.Home, GlobalName)
	target := p // where project-scoped gists go
	switch {
	case p.Name == "":
		target = Store(p.Home, UnfiledName)
	case p.IsGlobal():
		target = global
	}
	start, err := ingestStart(p.Home, s, target, global)
	if err != nil {
		return res, err
	}
	fresh := s.Since(start)
	res.Through = start
	if len(fresh) == 0 {
		res.Skip = "no new messages"
		return res, moveCursor(p.Home, s, start, now)
	}
	res.Through = fresh[len(fresh)-1].Line
	if userChars(fresh) < minUserChars {
		res.Skip = "too little user text"
		p.Log(Event{At: now, Kind: "ingest", Agent: s.Agent, Session: s.ID, Note: "skipped: " + res.Skip})
		return res, moveCursor(p.Home, s, res.Through, now)
	}

	began := time.Now()
	prompt, cut := ingestPrompt(p, s, fresh, target, global)
	var reply struct {
		Gists []struct {
			Gist  string
			Line  int
			Scope Scope
		}
	}
	if err := completeJSON(ctx, m, ingestSystem, prompt, ingestSchema, &reply); err != nil {
		p.Log(Event{At: now, Kind: "ingest", Agent: s.Agent, Session: s.ID, Ms: time.Since(began).Milliseconds(), Err: errString(err)})
		return res, fmt.Errorf("ingest %s: %w", s.Path, err)
	}

	byLine := map[int]transcript.Message{}
	for _, msg := range fresh {
		byLine[msg.Line] = msg
	}
	var toTarget, toGlobal []Entry
	var invalid []string
	for _, g := range reply.Gists {
		msg, ok := byLine[g.Line]
		if !ok {
			invalid = append(invalid, fmt.Sprintf("line %d isn't a new message", g.Line))
			continue
		}
		gist, err := ParseGist(g.Gist)
		if err != nil {
			invalid = append(invalid, err.Error())
			continue
		}
		at := msg.Time
		if at.IsZero() {
			at = now
		}
		e := Entry{ID: NewEntryID(now), At: at.UTC(), Session: s.ID, Folder: orDefault(s.Cwd, p.Cwd),
			Source: Pointer{Kind: sourceKind(s.Agent), Path: s.Path, Line: msg.Line}, Gist: gist}
		if g.Scope == ScopeGlobal {
			toGlobal = append(toGlobal, e)
		} else {
			toTarget = append(toTarget, e)
		}
	}
	if err := target.Append(toTarget); err != nil {
		return res, err
	}
	if err := global.Append(toGlobal); err != nil {
		return res, err
	}
	if target.IsUnfiled() {
		res.Unfiled = len(toTarget)
	} else if target.IsGlobal() {
		res.Global += len(toTarget)
	} else {
		res.Project = len(toTarget)
	}
	res.Global += len(toGlobal)

	note := fmt.Sprintf("%d new messages: %d to %s, %d to global", len(fresh), len(toTarget), target.Name, len(toGlobal))
	if cut > 0 {
		note += fmt.Sprintf("; earliest %d characters cut to fit the window", cut)
	}
	if len(invalid) > 0 {
		note += fmt.Sprintf("; %d dropped (%s)", len(invalid), strings.Join(invalid, "; "))
	}
	p.Log(Event{At: now, Kind: "ingest", Agent: s.Agent, Session: s.ID, Ms: time.Since(began).Milliseconds(), Note: note})
	return res, moveCursor(p.Home, s, res.Through, now)
}

// ingestStart is the line to read after: the cursor, or the last line an
// entry of this session cites if that is later (a pass that appended and
// died before moving the cursor).
func ingestStart(home string, s transcript.Session, stores ...Project) (int, error) {
	cursors, err := Cursors(home)
	if err != nil {
		return 0, err
	}
	start := 0
	if c := cursors[s.Path]; c != nil {
		start = c.Line
	}
	for _, st := range stores {
		es, err := st.Entries()
		if err != nil {
			return 0, err
		}
		for _, e := range es {
			if e.Session == s.ID && e.Source.Path == s.Path && e.Source.Line > start {
				start = e.Source.Line
			}
		}
	}
	return start, nil
}

func moveCursor(home string, s transcript.Session, line int, now time.Time) error {
	return updateCursors(home, func(c map[string]*Cursor) {
		if cur := c[s.Path]; cur == nil || cur.Line < line {
			c[s.Path] = &Cursor{Agent: s.Agent, Session: s.ID, Line: line, At: now.UTC()}
		} else {
			cur.At = now.UTC()
		}
	})
}

func userChars(ms []transcript.Message) int {
	n := 0
	for _, m := range ms {
		if m.Role == "user" {
			n += len(strings.TrimSpace(m.Text))
		}
	}
	return n
}

func sourceKind(agent string) SourceKind {
	switch agent {
	case "codex":
		return FromCodex
	case "opencode":
		return FromOpenCode
	}
	return FromClaude
}

// recentNotes is how many of each store's newest entries ingest is shown.
const recentNotes = 30

// ingestPrompt lays out the session, the notes already taken and the new
// messages, newest kept when they don't fit. It returns how many characters
// of the oldest messages were cut.
func ingestPrompt(p Project, s transcript.Session, fresh []transcript.Message, stores ...Project) (string, int) {
	var b strings.Builder
	b.WriteString("## Session\n\n")
	fmt.Fprintf(&b, "Folder: %s\n", orDefault(s.Cwd, p.Cwd))
	switch {
	case p.Name == "":
		b.WriteString("Project: none linked to this folder\n")
	default:
		fmt.Fprintf(&b, "Project: %s", p.Name)
		if d := p.Meta().Description; d != "" {
			b.WriteString(" (" + d + ")")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Notes already taken (newest last)\n\n")
	noted := false
	for _, st := range stores {
		es, _ := st.Entries()
		for _, e := range es[max(0, len(es)-recentNotes):] {
			fmt.Fprintf(&b, "- [%s] %s\n", st.Name, e.Gist)
			noted = true
		}
	}
	if !noted {
		b.WriteString("(none)\n")
	}

	var msgs strings.Builder
	for _, m := range fresh {
		text := m.Text
		if r := []rune(text); len(r) > messageChars {
			text = string(r[:messageChars]) + " …(cut)"
		}
		fmt.Fprintf(&msgs, "[line %d · %s · %s]\n%s\n\n", m.Line, m.Time.UTC().Format("2006-01-02 15:04Z"), m.Role, text)
	}
	text, cut := msgs.String(), 0
	if len(text) > ingestWindow {
		cut = len(text) - ingestWindow
		for cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut++
		}
		text = "…(earlier messages cut)\n\n" + text[cut:]
	}
	fmt.Fprintf(&b, "\n## New messages (%d)\n\n%s", len(fresh), text)
	return b.String(), cut
}
