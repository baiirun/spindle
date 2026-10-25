package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"spindle/internal/model"
	"spindle/internal/source"
)

// Client is the one way in to memory for anything outside the hooks: the
// MCP tools and the CLI are thin wrappers over it, so they can do the same
// things and print the same text. Each call goes to the activity log with the
// caller (claude, codex or cli). The session's project is worked out from the
// folder on every call, so linking a folder mid-session takes effect at once.
type Client struct {
	Home   string
	Cwd    string // the session's folder
	Pin    string // a project name that overrides the folder's hint
	Caller string
}

// NewClient is a client for a session in cwd; project, if set, pins the
// session's project.
func NewClient(home, cwd, project, caller string) Client {
	return Client{Home: home, Cwd: cwd, Pin: project, Caller: caller}
}

// Client returns a client pinned to p.
func (p Project) Client(caller string) Client {
	return Client{Home: p.Home, Cwd: p.Cwd, Pin: p.Name, Caller: caller}
}

// Context is the session's project (Name "" if the folder has none).
func (c Client) Context() Project {
	if c.Pin != "" {
		p := Store(c.Home, c.Pin)
		p.Cwd = c.Cwd
		return p
	}
	p, err := Hint(c.Home, c.Cwd)
	if err != nil {
		return Project{Home: c.Home, Cwd: c.Cwd}
	}
	return p
}

// Read returns one memory in full.
func (c Client) Read(name string) (Memory, error) {
	start := time.Now()
	p := c.Context()
	m, ok := p.GetRef(name)
	var err error
	if !ok {
		err = fmt.Errorf("no memory %q", m.Name)
	}
	c.log(p, "read", []string{m.Name + ".md"}, "", "", start, err)
	return m, err
}

// Write creates or replaces a memory and says what happened.
func (c Client) Write(o Op) (string, error) {
	o.Op = "write"
	return c.apply(o)
}

// Delete removes a memory.
func (c Client) Delete(name, reason string) (string, error) {
	return c.apply(Op{Op: "delete", Name: name, Reason: reason})
}

func (c Client) apply(o Op) (string, error) {
	start := time.Now()
	p := c.Context()
	ref := o.Name
	msg, err := func() (string, error) {
		s, bareOp, err := p.route(o)
		if err != nil {
			return "", err
		}
		ref = RefOf(s, NameOf(bareOp.Name))
		existed := s.Exists()
		by := "agent" // extraction skips messages the agent already saved for
		if c.Caller == "cli" {
			by = "cli"
		}
		res, err := p.ApplyAll([]Op{o}, by, "", time.Now())
		if err != nil {
			return "", err
		}
		switch {
		case res.Written > 0:
			msg := "Saved " + ref + ".md."
			if !existed && !s.IsGlobal() {
				msg += " New project " + s.Name + "."
			}
			return msg, nil
		case res.Deleted > 0:
			return "Deleted " + ref + ".md.", nil
		case o.Op == "delete":
			return "", fmt.Errorf("no memory %q", ref)
		case res.Skipped > 0:
			return "", fmt.Errorf("not saved: name, a valid type, description and body are all required")
		}
		return "Unchanged.", nil
	}()
	c.log(p, o.Op, []string{ref + ".md"}, "", oneLine(o.Reason), start, err)
	return msg, err
}

// Search ranks memories by the query's words; total is how many memories exist.
func (c Client) Search(query string, limit int) (hits []Memory, total int, err error) {
	start := time.Now()
	p := c.Context()
	mems, err := p.LoadAll()
	if err == nil {
		hits, total = Search(mems, query, limit), len(mems)
	}
	c.log(p, "search", nil, query, fmt.Sprintf("%d of %d match", len(hits), total), start, err)
	return hits, total, err
}

// Index is what a session starts with, uncut: the global index, this
// project's, and a line per other project.
func (c Client) Index() string {
	p := c.Context()
	c.log(p, "index", nil, "", "", time.Now(), nil)
	return p.indexText()
}

// Status summarizes the session's project and the rest of memory.
type Status struct {
	Project    Meta // Name "" if the folder has none
	MemDir     string
	Total      int
	Types      map[string]int
	Global     int // memories in the global store
	Projects   int // other projects
	Elsewhere  int // their memories
	LastDream  time.Time
	SinceDream int // sessions since the last dream
	Cursors    map[string]*Cursor
}

func (c Client) Status() (Status, error) {
	p := c.Context()
	s := Status{Project: p.Meta(), Types: map[string]int{}}
	if p.Name != "" {
		s.MemDir = p.MemDir()
	}
	mems, err := p.Load()
	if err != nil && p.Name != "" {
		return s, err
	}
	st, err := p.LoadState()
	if err != nil {
		return s, err
	}
	for _, m := range mems {
		s.Types[m.Type]++
	}
	s.Total, s.LastDream, s.SinceDream = len(mems), st.LastDream, len(st.Sessions)
	if !p.IsGlobal() {
		glob, _ := Store(c.Home, GlobalName).Load()
		s.Global = len(glob)
	}
	ps, _ := Projects(c.Home)
	for _, o := range ps {
		if o.Name != p.Name {
			mems, _ := o.Load()
			s.Projects++
			s.Elsewhere += len(mems)
		}
	}
	if s.Cursors, err = Cursors(c.Home); err != nil {
		return s, err
	}
	return s, nil
}

// Projects lists global and every project.
func (c Client) Projects() ([]ProjectInfo, error) {
	p := c.Context()
	c.log(p, "projects", nil, "", "", time.Now(), nil)
	return p.ProjectList()
}

// SetProject creates or describes a project and links or unlinks folders.
// link "." means the session's folder (its repository root).
func (c Client) SetProject(name, description, link, unlink string) (string, error) {
	start := time.Now()
	ctx := c.Context()
	s := Store(c.Home, name)
	msg, err := func() (string, error) {
		if s.Name == "" || s.IsGlobal() {
			return "", fmt.Errorf("%q can't be a project name", name)
		}
		existed := s.Exists()
		if err := s.UpdateMeta(func(m *Meta) {
			if d := oneLine(description); d != "" {
				m.Description = d
			}
		}); err != nil {
			return "", err
		}
		var notes []string
		if !existed {
			notes = append(notes, "Created project "+s.Name+".")
		} else if description != "" {
			notes = append(notes, "Described "+s.Name+".")
		}
		if link == "." {
			link = gitRoot(c.Cwd)
		}
		if link != "" {
			if err := s.Link(link); err != nil {
				return "", err
			}
			notes = append(notes, "Linked "+link+".")
		}
		if unlink != "" {
			if err := s.Unlink(unlink); err != nil {
				return "", err
			}
			notes = append(notes, "Unlinked "+unlink+".")
		}
		if len(notes) == 0 {
			notes = append(notes, "Unchanged.")
		}
		return strings.Join(notes, " "), nil
	}()
	c.log(ctx, "project", nil, "", s.Name, start, err)
	return msg, err
}

// Sources lists the configured sources and how far each has been swept.
func (c Client) Sources() (string, error) {
	start := time.Now()
	p := c.Context()
	srcs, cfgErr := source.Load(c.Home)
	marks, err := Marks(c.Home)
	c.log(p, "sources", nil, "", "", start, err)
	if err != nil {
		return "", err
	}
	var infos []SourceInfo
	for _, src := range srcs {
		infos = append(infos, sourceInfo(src, marks))
	}
	return FormatSources(source.ConfigPath(c.Home), infos, cfgErr), nil
}

// AddSource adds a source to sources.json, or points an existing one at
// another folder. Either way it starts with the sessions that change from now
// on. With no sources.json yet, the defaults are written out first.
func (c Client) AddSource(kind, dir string) (string, error) {
	start := time.Now()
	msg, err := c.editSources(func(cfg *source.Config) (string, error) {
		if dir != "" && !strings.HasPrefix(dir, "~") {
			abs, err := filepath.Abs(dir)
			if err != nil {
				return "", err
			}
			dir = abs
		}
		spec := source.Spec{Kind: kind, Dir: dir, Added: start.UTC()}
		if _, err := source.New(spec); err != nil {
			return "", err
		}
		for i, s := range cfg.Sources {
			if s.Kind != kind {
				continue
			}
			if s.Dir == dir {
				return fmt.Sprintf("%s is already a source.", kind), nil
			}
			cfg.Sources[i] = spec
			return fmt.Sprintf("Pointed %s at %s; it reads sessions that change from now on.", kind, orDefault(dir, "its default folder")), nil
		}
		cfg.Sources = append(cfg.Sources, spec)
		return fmt.Sprintf("Added %s; it reads sessions that change from now on.", kind), nil
	})
	c.log(c.Context(), "source_add", nil, "", kind, start, err)
	return msg, err
}

// RemoveSource takes a source out of sources.json. Its sessions are no longer
// swept; what was learned from them stays.
func (c Client) RemoveSource(kind string) (string, error) {
	start := time.Now()
	msg, err := c.editSources(func(cfg *source.Config) (string, error) {
		for i, s := range cfg.Sources {
			if s.Kind == kind {
				cfg.Sources = append(cfg.Sources[:i], cfg.Sources[i+1:]...)
				return fmt.Sprintf("Removed %s; its sessions are no longer swept, and its memories stay.", kind), nil
			}
		}
		return "", fmt.Errorf("no source %q", kind)
	})
	c.log(c.Context(), "source_remove", nil, "", kind, start, err)
	return msg, err
}

func (c Client) editSources(f func(*source.Config) (string, error)) (string, error) {
	unlock, err := lockFile(filepath.Join(c.Home, "sources.lock"), true)
	if err != nil {
		return "", err
	}
	defer unlock()
	cfg, err := source.LoadConfig(c.Home)
	if err != nil {
		return "", err
	}
	cfg.Sources = slices.Clone(cfg.Sources)
	note, err := f(&cfg)
	if err != nil {
		return "", err
	}
	if err := source.SaveConfig(c.Home, cfg); err != nil {
		return "", err
	}
	list, err := c.Sources()
	return note + "\n\n" + list, err
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (c Client) log(p Project, op string, files []string, query, note string, start time.Time, err error) {
	p.Log(Event{Kind: "call", Agent: c.Caller, Name: op, Files: files, Prompt: query, Note: note, Ms: time.Since(start).Milliseconds(), Err: errString(err)})
}

// The text both interfaces show.

func FormatMemory(m Memory) string {
	return fmt.Sprintf("Memory: %s (modified %s):\n\n%s", m.File(), m.Modified.UTC().Format("2006-01-02"), bare(m).Render())
}

func FormatSearch(hits []Memory, total int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d memories match.\n", len(hits), total)
	for _, m := range hits {
		fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", m.Type, m.File(), m.Modified.UTC().Format("2006-01-02"), m.Description)
	}
	return b.String()
}

func FormatStatus(s Status) string {
	var b strings.Builder
	if s.Project.Name == "" {
		b.WriteString("project  none for this folder (spin project NAME --link . to link one)\n")
	} else {
		fmt.Fprintf(&b, "project  %s", s.Project.Name)
		if s.Project.Description != "" {
			b.WriteString(": " + s.Project.Description)
		}
		fmt.Fprintf(&b, "\nmemory   %s\n", s.MemDir)
		for _, f := range s.Project.Folders {
			fmt.Fprintf(&b, "folder   %s\n", f)
		}
		fmt.Fprintf(&b, "files    %d (user %d, directive %d, project %d, reference %d)\n", s.Total, s.Types["user"], s.Types["directive"], s.Types["project"], s.Types["reference"])
	}
	if s.Project.Name != GlobalName {
		fmt.Fprintf(&b, "global   %d, shared by every project\n", s.Global)
	}
	fmt.Fprintf(&b, "others   %d in %d other projects (spin projects)\n", s.Elsewhere, s.Projects)
	if s.Project.Name == "" {
		return b.String()
	}
	last := "never"
	if !s.LastDream.IsZero() {
		last = s.LastDream.Local().Format("2006-01-02 15:04")
	}
	fmt.Fprintf(&b, "dream    last %s; %d of %d sessions since, due after %s\n", last, s.SinceDream, DreamSessions, DreamEvery)
	paths := make([]string, 0, len(s.Cursors))
	for path := range s.Cursors {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool { return s.Cursors[paths[i]].At.After(s.Cursors[paths[j]].At) })
	if len(paths) > 5 {
		fmt.Fprintf(&b, "cursors  %d transcripts read; the latest:\n", len(paths))
		paths = paths[:5]
	}
	for _, path := range paths {
		c := s.Cursors[path]
		fmt.Fprintf(&b, "cursor   %s line %d, %s (%s)\n", c.Agent, c.Line, c.At.Local().Format("2006-01-02 15:04"), path)
	}
	return b.String()
}

// Dream runs the consolidation pass now. Unless force is set it waits for the
// gate (24 h and 5 sessions since the last dream).
func (c Client) Dream(ctx context.Context, m model.Model, force bool) (DreamResult, error) {
	p := c.Context()
	if p.Name == "" {
		return DreamResult{}, errNoProject
	}
	st, err := p.LoadState()
	if err != nil {
		return DreamResult{}, err
	}
	if !force && !DreamDue(st, time.Now()) {
		return DreamResult{}, fmt.Errorf("not due: %d of %d sessions since the last dream; --force to run anyway", len(st.Sessions), DreamSessions)
	}
	return Dream(ctx, p, m, time.Now())
}

// Extract runs one extraction pass over a transcript now.
func (c Client) Extract(ctx context.Context, transcript string, m model.Model) (ExtractResult, error) {
	return Extract(ctx, c.Context(), transcript, m, time.Now())
}

// LogFilter narrows the activity log.
type LogFilter struct {
	Session string // prefix
	Kind    string
	Project string
	Last    int // 0: all
}

// Activity returns the activity log, oldest first, filtered.
func (c Client) Activity(f LogFilter) ([]Event, error) {
	evs, err := c.Context().Events()
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range evs {
		if f.Match(e) {
			out = append(out, e)
		}
	}
	if f.Last > 0 && len(out) > f.Last {
		out = out[len(out)-f.Last:]
	}
	return out, nil
}

func (f LogFilter) Match(e Event) bool {
	return strings.HasPrefix(e.Session, f.Session) && (f.Kind == "" || e.Kind == f.Kind) && (f.Project == "" || e.Project == f.Project)
}

// History returns one memory's changes, newest first. Old versions are for
// people auditing memory; they are never put into context on their own.
func (c Client) History(name string) ([]Change, error) {
	p := c.Context()
	store, name := p.locateLogged(name)
	start := time.Now()
	all, err := store.Changes()
	var out []Change
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Name == name {
			out = append(out, all[i])
		}
	}
	c.log(p, "history", []string{RefOf(store, name) + ".md"}, "", fmt.Sprintf("%d changes", len(out)), start, err)
	return out, err
}

// Restore puts back the version a change replaced or deleted: n counts from 1
// in History's order.
func (c Client) Restore(name string, n int) (string, error) {
	hist, err := c.History(name)
	if err != nil {
		return "", err
	}
	if n < 1 || n > len(hist) {
		return "", fmt.Errorf("%s has %d changes; pick 1-%d", name, len(hist), len(hist))
	}
	ch := hist[n-1]
	if ch.Old == "" {
		return "", errors.New("that change created the memory; there is no earlier version")
	}
	old, err := Parse(ch.Old)
	if err != nil {
		return "", err
	}
	store, _ := c.Context().locateLogged(name)
	return c.apply(Op{Op: "write", Name: RefOf(store, old.Name), Type: old.Type, Description: old.Description, Body: old.Body,
		Reason: fmt.Sprintf("restored the version before the %s of %s", ch.Op, ch.At.UTC().Format(time.RFC3339))})
}

func FormatEvents(evs []Event) string {
	if len(evs) == 0 {
		return "No activity.\n"
	}
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.String() + "\n")
	}
	return b.String()
}

// FormatHistory lists changes; with full set it includes each old version.
func FormatHistory(name string, hist []Change, full bool) string {
	if len(hist) == 0 {
		return fmt.Sprintf("No changes recorded for %s.\n", strings.TrimSpace(name))
	}
	var b strings.Builder
	for i, ch := range hist {
		what := map[string]string{"write": "changed", "delete": "deleted"}[ch.Op]
		if ch.Op == "write" && ch.Old == "" {
			what = "created"
		}
		fmt.Fprintf(&b, "%d. %s  %s by %s", i+1, ch.At.Local().Format("2006-01-02 15:04"), what, ch.By)
		if ch.Session != "" {
			fmt.Fprintf(&b, " (session %s)", clipRunes(ch.Session, 8))
		}
		if ch.Reason != "" {
			b.WriteString(": " + ch.Reason)
		}
		b.WriteString("\n")
		if full && ch.Old != "" {
			fmt.Fprintf(&b, "   before:\n%s\n", indent(ch.Old, "   | "))
		}
	}
	return b.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
