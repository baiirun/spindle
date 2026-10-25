package memory

import (
	"context"
	"encoding/json"
	"fmt"
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

// State is the project's bookkeeping.
type State struct {
	Cursors   map[string]*Cursor `json:"cursors"` // by transcript path
	LastDream time.Time          `json:"last_dream"`
	// Sessions that touched the project since the last dream, with their transcripts.
	Sessions map[string]string `json:"sessions_since_dream"`
}

func (p Project) statePath() string { return filepath.Join(p.Dir, "state.json") }

func (p Project) LoadState() (State, error) {
	s := State{}
	err := readJSON(p.statePath(), &s)
	if s.Cursors == nil {
		s.Cursors = map[string]*Cursor{}
	}
	if s.Sessions == nil {
		s.Sessions = map[string]string{}
	}
	return s, err
}

func (p Project) SaveState(s State) error { return writeJSON(p.statePath(), s) }

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
// cursor. Model calls run without the project lock; writes take it.
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
	if err := p.Init(); err != nil {
		return res, err
	}
	state, err := p.LoadState()
	if err != nil {
		return res, err
	}
	cur := state.Cursors[path]
	if cur == nil {
		cur = &Cursor{Agent: s.Agent, Session: s.ID}
	}
	fresh := s.Since(cur.Line)
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
	case p.agentWroteSince(fresh[0].Time):
		res.Skip = "agent wrote memory"
	}
	if res.Skip == "" {
		ops, err := extractOps(ctx, p, m, fresh)
		if err != nil {
			return res, err
		}
		unlock, err := p.Lock()
		if err != nil {
			return res, err
		}
		res.Result, err = p.Apply(ops, "extract", s.ID, now)
		unlock()
		if err != nil {
			return res, err
		}
	}
	if len(fresh) == 0 {
		return res, nil
	}
	unlock, err := p.Lock()
	if err != nil {
		return res, err
	}
	defer unlock()
	if state, err = p.LoadState(); err != nil {
		return res, err
	}
	cur = state.Cursors[path]
	if cur == nil {
		cur = &Cursor{Agent: s.Agent, Session: s.ID}
	}
	cur.Line, cur.At = fresh[len(fresh)-1].Line, now.UTC()
	state.Cursors[path] = cur
	state.Sessions[s.ID] = path
	return res, p.SaveState(state)
}

// extractOps asks for the files to read, then for the writes.
func extractOps(ctx context.Context, p Project, m model.Model, fresh []transcript.Message) ([]Op, error) {
	mems, err := p.Load()
	if err != nil {
		return nil, err
	}
	msgs := transcript.Render(fresh, messageChars)
	if len(msgs) > extractWindow {
		msgs = "…(earlier messages cut)\n\n" + msgs[len(msgs)-extractWindow:]
	}
	list := fileList(mems, 0)
	var step1 struct {
		NothingToSave bool     `json:"nothing_to_save"`
		Read          []string `json:"read"`
	}
	prompt := fmt.Sprintf("## Existing memory files\n\n%s\n## New messages (~%d)\n\n%s", orNone(list), len(fresh), msgs)
	if err := completeJSON(ctx, m, extractReadSystem, prompt, extractReadSchema, &step1); err != nil {
		return nil, fmt.Errorf("extract step 1: %w", err)
	}
	if step1.NothingToSave {
		return nil, nil
	}
	var files strings.Builder
	for i, name := range step1.Read {
		if i >= readFilesLimit {
			break
		}
		if mem, ok := p.Get(name); ok {
			fmt.Fprintf(&files, "<file name=%q>\n%s</file>\n\n", mem.File(), mem.Render())
		}
	}
	var step2 struct {
		Ops []Op `json:"ops"`
	}
	prompt = fmt.Sprintf("## Files you read\n\n%s\n## All memory files\n\n%s\n## New messages (~%d)\n\n%s", orNone(files.String()), orNone(list), len(fresh), msgs)
	if err := completeJSON(ctx, m, extractWriteSystem, prompt, opsSchema, &step2); err != nil {
		return nil, fmt.Errorf("extract step 2: %w", err)
	}
	return step2.Ops, nil
}

// agentWroteSince reports whether the main agent wrote memory after t, in
// which case the extraction pass would only second-guess it.
func (p Project) agentWroteSince(t time.Time) bool {
	changes, _ := p.Changes()
	for i := len(changes) - 1; i >= 0; i-- {
		c := changes[i]
		if c.At.Before(t) {
			break
		}
		if c.By == "agent" {
			return true
		}
	}
	return false
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
