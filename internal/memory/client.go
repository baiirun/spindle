package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"spindle/internal/model"
)

// Client is the one way in to a project's memory for anything outside the
// hooks: the MCP tools and the CLI are thin wrappers over it, so they can do
// the same things and print the same text. Each call goes to the activity log
// with the caller (claude, codex or cli).
type Client struct {
	Project
	Caller string
}

// Client returns a client for caller.
func (p Project) Client(caller string) Client { return Client{p, caller} }

// Read returns one memory in full.
func (c Client) Read(name string) (Memory, error) {
	start := time.Now()
	m, ok := c.GetRef(name)
	var err error
	if !ok {
		err = fmt.Errorf("no memory %q", m.Name)
	}
	c.log("read", []string{m.Name + ".md"}, "", "", start, err)
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
	msg, err := func() (string, error) {
		unlock, err := c.Lock()
		if err != nil {
			return "", err
		}
		defer unlock()
		by := "agent" // extraction skips messages the agent already saved for
		if c.Caller == "cli" {
			by = "cli"
		}
		res, err := c.ApplyAll([]Op{o}, by, "", time.Now())
		if err != nil {
			return "", err
		}
		s, bare := c.scopeFor(o)
		name := RefOf(s, NameOf(bare.Name))
		switch {
		case res.Written > 0:
			return "Saved " + name + ".md.", nil
		case res.Deleted > 0:
			return "Deleted " + name + ".md.", nil
		case o.Op == "delete":
			return "", fmt.Errorf("no memory %q", name)
		case res.Skipped > 0:
			return "", fmt.Errorf("not saved: name, a valid type, description and body are all required")
		}
		return "Unchanged.", nil
	}()
	s, bare := c.scopeFor(o)
	c.log(o.Op, []string{RefOf(s, NameOf(bare.Name)) + ".md"}, "", oneLine(o.Reason), start, err)
	return msg, err
}

// Search ranks memories by the query's words; total is how many memories exist.
func (c Client) Search(query string, limit int) (hits []Memory, total int, err error) {
	start := time.Now()
	mems, err := c.LoadAll()
	if err == nil {
		hits, total = Search(mems, query, limit), len(mems)
	}
	c.log("search", nil, query, fmt.Sprintf("%d of %d match", len(hits), total), start, err)
	return hits, total, err
}

// Index is MEMORY.md as it is now, or a line saying there is nothing yet.
func (c Client) Index() string {
	c.log("index", nil, "", "", time.Now(), nil)
	return c.indexText()
}

// indexText is the project's index, then the global one.
func (p Project) indexText() string {
	var b strings.Builder
	if s := strings.TrimSpace(p.ReadIndex()); s != "" {
		b.WriteString(s + "\n")
	} else {
		fmt.Fprintf(&b, "No memories yet for %s.\n", p.Root)
	}
	if g := strings.TrimSpace(p.GlobalIndex()); g != "" {
		b.WriteString("\n# Global memory (every project)\n\n" + g + "\n")
	}
	return b.String()
}

// Status summarizes the project's memory and bookkeeping.
type Status struct {
	Root, MemDir string
	Total        int
	Types        map[string]int
	Global       int // memories in the global store
	LastDream    time.Time
	SinceDream   int // sessions since the last dream
	Cursors      map[string]*Cursor
}

func (c Client) Status() (Status, error) {
	s := Status{Root: c.Root, MemDir: c.MemDir(), Types: map[string]int{}}
	mems, err := c.Load()
	if err != nil {
		return s, err
	}
	st, err := c.LoadState()
	if err != nil {
		return s, err
	}
	for _, m := range mems {
		s.Types[m.Type]++
	}
	s.Total, s.LastDream, s.SinceDream, s.Cursors = len(mems), st.LastDream, len(st.Sessions), st.Cursors
	if glob, err := c.Global().Load(); err == nil && !c.IsGlobal() {
		s.Global = len(glob)
	}
	return s, nil
}

func (c Client) log(op string, files []string, query, note string, start time.Time, err error) {
	c.Log(Event{Kind: "call", Agent: c.Caller, Name: op, Files: files, Prompt: query, Note: note, Ms: time.Since(start).Milliseconds(), Err: errString(err)})
}

// The text both interfaces show.

func FormatMemory(m Memory) string {
	bare := m
	bare.Name = strings.TrimPrefix(m.Name, GlobalPrefix)
	return fmt.Sprintf("Memory: %s (modified %s):\n\n%s", m.File(), m.Modified.UTC().Format("2006-01-02"), bare.Render())
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
	fmt.Fprintf(&b, "project  %s\nmemory   %s\n", s.Root, s.MemDir)
	fmt.Fprintf(&b, "files    %d (user %d, directive %d, project %d, reference %d)\n", s.Total, s.Types["user"], s.Types["directive"], s.Types["project"], s.Types["reference"])
	fmt.Fprintf(&b, "global   %d, shared by every project\n", s.Global)
	last := "never"
	if !s.LastDream.IsZero() {
		last = s.LastDream.Local().Format("2006-01-02 15:04")
	}
	fmt.Fprintf(&b, "dream    last %s; %d of %d sessions since, due after %s\n", last, s.SinceDream, DreamSessions, DreamEvery)
	paths := make([]string, 0, len(s.Cursors))
	for path := range s.Cursors {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		c := s.Cursors[path]
		fmt.Fprintf(&b, "cursor   %s line %d, %s (%s)\n", c.Agent, c.Line, c.At.Local().Format("2006-01-02 15:04"), path)
	}
	return b.String()
}

// Dream runs the consolidation pass now. Unless force is set it waits for the
// gate (24 h and 5 sessions since the last dream).
func (c Client) Dream(ctx context.Context, m model.Model, force bool) (DreamResult, error) {
	st, err := c.LoadState()
	if err != nil {
		return DreamResult{}, err
	}
	if !force && !DreamDue(st, time.Now()) {
		return DreamResult{}, fmt.Errorf("not due: %d of %d sessions since the last dream; --force to run anyway", len(st.Sessions), DreamSessions)
	}
	return Dream(ctx, c.Project, m, time.Now())
}

// Extract runs one extraction pass over a transcript now.
func (c Client) Extract(ctx context.Context, transcript string, m model.Model) (ExtractResult, error) {
	return Extract(ctx, c.Project, transcript, m, time.Now())
}

// LogFilter narrows the activity log.
type LogFilter struct {
	Session string // prefix
	Kind    string
	Last    int // 0: all
}

// Activity returns the activity log, oldest first, filtered.
func (c Client) Activity(f LogFilter) ([]Event, error) {
	evs, err := c.Events()
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
	return strings.HasPrefix(e.Session, f.Session) && (f.Kind == "" || e.Kind == f.Kind)
}

// History returns one memory's changes, newest first. Old versions are for
// people auditing memory; they are never put into context on their own.
func (c Client) History(name string) ([]Change, error) {
	store, name := c.ref(name)
	start := time.Now()
	all, err := store.Changes()
	var out []Change
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Name == name {
			out = append(out, all[i])
		}
	}
	c.log("history", []string{RefOf(store, name) + ".md"}, "", fmt.Sprintf("%d changes", len(out)), start, err)
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
	store, _ := c.ref(name)
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
		return fmt.Sprintf("No changes recorded for %s.\n", NameOf(name))
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
