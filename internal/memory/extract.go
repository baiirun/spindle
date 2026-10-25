package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindle/internal/model"
	"spindle/internal/transcript"
)

// Cursor is how far extraction has read one transcript.
type Cursor struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Line    int       `json:"line"`
	At      time.Time `json:"at"` // last pass
}

// State is a store's dream gate.
type State struct {
	LastDream time.Time `json:"last_dream"`
	// Sessions that touched the store since the last dream, with their transcripts.
	Sessions map[string]string `json:"sessions_since_dream"`
}

func (p Project) statePath() string { return filepath.Join(p.Dir, "state.json") }

func (p Project) LoadState() (State, error) {
	s := State{}
	var err error
	if p.Name != "" {
		err = readJSON(p.statePath(), &s)
	}
	if s.Sessions == nil {
		s.Sessions = map[string]string{}
	}
	return s, err
}

func (p Project) SaveState(s State) error { return writeJSON(p.statePath(), s) }

// markSession records that a session touched the store, for the dream gate.
func (p Project) markSession(session, transcript string) error {
	if !p.Exists() {
		return nil
	}
	unlock, err := p.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := p.LoadState()
	if err != nil {
		return err
	}
	st.Sessions[session] = transcript
	return p.SaveState(st)
}

// Cursors reads how far extraction has read each transcript.
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

// Extraction thresholds, after Claude Code's: it runs after every turn, but
// not when the user wrote too little to be worth a pass, and not when the main
// agent already wrote memory for those messages.
const (
	minUserChars   = 20     // new user text below this skips the pass ("go", "ok")
	extractWindow  = 60_000 // characters of new messages sent; older ones in a long gap are cut
	messageChars   = 6_000  // one message is cut at this many characters
	readFilesLimit = 20
)

// ExtractResult says what a pass did.
type ExtractResult struct {
	Skip string // why nothing ran: "no new messages", "too little user text", "agent wrote memory"
	Result
}

// Extract runs one extraction pass over a transcript's messages since its
// cursor. p is the session's context: its project, which may be none, and
// folder. Model calls run without locks; each store's writes take its lock.
func Extract(ctx context.Context, p Project, path string, m model.Model, now time.Time) (ExtractResult, error) {
	var res ExtractResult
	s, err := transcript.Read(path)
	if err != nil {
		return res, err
	}
	if s.Subagent {
		res.Skip = "subagent"
		return res, nil
	}
	cursors, err := Cursors(p.Home)
	if err != nil {
		return res, err
	}
	line := 0
	if cur := cursors[path]; cur != nil {
		line = cur.Line
	}
	fresh := s.Since(line)
	userChars := 0
	for _, msg := range fresh {
		if msg.Role == "user" {
			userChars += len(strings.TrimSpace(msg.Text))
		}
	}
	switch {
	case len(fresh) == 0:
		res.Skip = "no new messages"
	case userChars < minUserChars:
		res.Skip = "too little user text"
	case p.wroteSince("agent", fresh[0].Time):
		res.Skip = "agent wrote memory"
	}
	if res.Skip == "" {
		start := time.Now()
		ev := Event{Kind: "extract", Agent: s.Agent, Session: s.ID, Note: fmt.Sprintf("%d new messages", len(fresh))}
		ops, cut, err := extractOps(ctx, p, m, fresh)
		if cut > 0 {
			ev.Note += fmt.Sprintf("; earliest %d characters cut to fit the %d-character window", cut, extractWindow)
		}
		if err == nil {
			res.Result, err = p.ApplyAll(ops, "extract", s.ID, now)
		}
		ev.Ms, ev.Files, ev.Err = time.Since(start).Milliseconds(), res.Changed, errString(err)
		if err == nil && len(res.Changed) == 0 {
			ev.Note += "; nothing worth saving"
		}
		p.Log(ev)
		if err != nil {
			return res, err
		}
	} else if len(fresh) > 0 {
		p.Log(Event{Kind: "skip", Agent: s.Agent, Session: s.ID, Name: "extract", Note: res.Skip})
	}
	if len(fresh) == 0 {
		return res, nil
	}
	// The dream gate counts the session for its project and for every store
	// it changed.
	touched := map[string]bool{p.Name: p.Name != ""}
	for _, c := range res.Changed {
		if st, _ := splitRef(c[1:]); st != "" {
			touched[st] = true
		}
	}
	for name, ok := range touched {
		if ok {
			if err := Store(p.Home, name).markSession(s.ID, path); err != nil {
				return res, err
			}
		}
	}
	return res, updateCursors(p.Home, func(c map[string]*Cursor) {
		c[path] = &Cursor{Agent: s.Agent, Session: s.ID, Line: fresh[len(fresh)-1].Line, At: now.UTC()}
	})
}

// extractOps asks for the files to read, then for the writes. It also
// returns how many characters of the oldest messages were cut to fit the window.
func extractOps(ctx context.Context, p Project, m model.Model, fresh []transcript.Message) ([]Op, int, error) {
	mems, err := p.LoadAll()
	if err != nil {
		return nil, 0, err
	}
	msgs := transcript.Render(fresh, messageChars)
	cut := 0
	if len(msgs) > extractWindow {
		cut = len(msgs) - extractWindow
		msgs = "…(earlier messages cut)\n\n" + msgs[cut:]
	}
	list := fileList(mems, 0)
	var step1 struct {
		NothingToSave bool     `json:"nothing_to_save"`
		Read          []string `json:"read"`
	}
	session := p.sessionText()
	prompt := fmt.Sprintf("%s## Existing memory files\n\n%s\n## New messages (~%d)\n\n%s", session, orNone(list), len(fresh), msgs)
	if err := completeJSON(ctx, m, extractReadSystem, prompt, extractReadSchema, &step1); err != nil {
		return nil, cut, fmt.Errorf("extract step 1: %w", err)
	}
	if step1.NothingToSave {
		return nil, cut, nil
	}
	var files strings.Builder
	for i, name := range step1.Read {
		if i >= readFilesLimit {
			break
		}
		if mem, ok := p.GetRef(name); ok {
			fmt.Fprintf(&files, "<file name=%q>\n%s</file>\n\n", mem.File(), bare(mem).Render())
		}
	}
	var step2 struct {
		Ops []Op `json:"ops"`
	}
	prompt = fmt.Sprintf("%s## Files you read\n\n%s\n## All memory files\n\n%s\n## New messages (~%d)\n\n%s", session, orNone(files.String()), orNone(list), len(fresh), msgs)
	if err := completeJSON(ctx, m, extractWriteSystem, prompt, opsSchema, &step2); err != nil {
		return nil, cut, fmt.Errorf("extract step 2: %w", err)
	}
	return step2.Ops, cut, nil
}

// sessionText tells a side pass which project the session is in and which
// projects exist.
func (p Project) sessionText() string {
	var b strings.Builder
	b.WriteString("## Session\n\n")
	if p.Cwd != "" {
		fmt.Fprintf(&b, "Folder: %s\n", p.Cwd)
	}
	if p.Name == "" {
		b.WriteString("Project: none linked to this folder\n")
	} else {
		fmt.Fprintf(&b, "Project: %s", p.Name)
		if d := p.Meta().Description; d != "" {
			b.WriteString(" (" + d + ")")
		}
		b.WriteString("\n")
	}
	ps, _ := Projects(p.Home)
	var names []string
	for _, o := range ps {
		names = append(names, o.Name)
	}
	fmt.Fprintf(&b, "All projects: %s, global\n\n", strings.Join(names, ", "))
	return b.String()
}

// fileList is the "[type] file (mtime): description" list Claude Code shows
// its extractor and selector, newest first, capped at n (0: no cap).
func fileList(mems []Memory, n int) string {
	var b strings.Builder
	for i, m := range mems {
		if n > 0 && i >= n {
			break
		}
		fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", m.Type, m.File(), m.Modified.UTC().Format(time.RFC3339), oneLine(m.Description))
	}
	return b.String()
}

func completeJSON(ctx context.Context, m model.Model, system, prompt, schema string, out any) error {
	text, err := m.Complete(ctx, system, prompt, schema)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(model.JSONObject(text)), out); err != nil {
		return fmt.Errorf("decode %q: %w", clipRunes(text, 200), err)
	}
	return nil
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)\n"
	}
	return s
}

// Changes reads the audit log.
func (p Project) Changes() ([]Change, error) {
	return readJSONL[Change](filepath.Join(p.Dir, "changes.jsonl"))
}

// DetectAgent says which tool wrote a transcript.
func DetectAgent(path string) (string, error) { return transcript.Detect(path) }

// TranscriptCwd is the working directory a transcript's session ran in.
func TranscriptCwd(path string) (string, error) {
	s, err := transcript.Read(path)
	if err != nil {
		return "", err
	}
	if s.Cwd == "" {
		return "", fmt.Errorf("%s: no cwd recorded; pass --project", path)
	}
	return s.Cwd, nil
}
