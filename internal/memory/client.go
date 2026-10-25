package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
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
	m, ok := c.Get(name)
	var err error
	if !ok {
		err = fmt.Errorf("no memory %q", NameOf(name))
	}
	c.log("read", []string{NameOf(name) + ".md"}, "", "", start, err)
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
		res, err := c.Apply([]Op{o}, by, "", time.Now())
		if err != nil {
			return "", err
		}
		name := NameOf(o.Name)
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
	c.log(o.Op, []string{NameOf(o.Name) + ".md"}, "", oneLine(o.Reason), start, err)
	return msg, err
}

// Search ranks memories by the query's words; total is how many memories exist.
func (c Client) Search(query string, limit int) (hits []Memory, total int, err error) {
	start := time.Now()
	mems, err := c.Load()
	if err == nil {
		hits, total = Search(mems, query, limit), len(mems)
	}
	c.log("search", nil, query, fmt.Sprintf("%d of %d match", len(hits), total), start, err)
	return hits, total, err
}

// Index is MEMORY.md as it is now.
func (c Client) Index() string {
	c.log("index", nil, "", "", time.Now(), nil)
	return c.ReadIndex()
}

// Status summarizes the project's memory and bookkeeping.
type Status struct {
	Root, MemDir string
	Total        int
	Types        map[string]int
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
	return s, nil
}

func (c Client) log(op string, files []string, query, note string, start time.Time, err error) {
	c.Log(Event{Kind: "call", Agent: c.Caller, Name: op, Files: files, Prompt: query, Note: note, Ms: time.Since(start).Milliseconds(), Err: errString(err)})
}

// The text both interfaces show.

func FormatMemory(m Memory) string {
	return fmt.Sprintf("Memory: %s (modified %s):\n\n%s", m.File(), m.Modified.UTC().Format("2006-01-02"), m.Render())
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
