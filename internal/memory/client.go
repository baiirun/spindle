package memory

import (
	"time"
)

// Client is the one way in to memory for anything outside the hooks and the
// worker: the MCP tools and the CLI are thin wrappers over it, so they can do
// the same things and print the same text. Each call goes to the activity log
// with the caller (claude, codex, opencode or cli). The session's project is
// worked out from the folder on every call, so linking a folder mid-session
// takes effect at once.
//
// Agents only append: nothing an MCP tool calls changes rules or the wiki.
// Page edits come from dream, or from a person through the CLI.
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

// Append adds one entry to the session's project, or to global when scope is
// ScopeGlobal or the folder has no project. note, if set, says where it came
// from. It returns the text both front ends print.
func (c Client) Append(gist string, scope Scope, note string) (string, error) {
	panic("unimplemented")
}

// Search searches every store; see Search.
func (c Client) Search(query string, layers []SearchLayer, limit int) (string, error) {
	panic("unimplemented")
}

// Zoom opens an id from the index; see Zoom.
func (c Client) Zoom(id string) (string, error) {
	panic("unimplemented")
}

// WikiRead returns one wiki page with its cited entries. A slug several
// stores have opens in each.
func (c Client) WikiRead(slug string) (string, error) {
	panic("unimplemented")
}

// RulesList returns the rule topics a session here loads: global's and the
// session's project's, in full.
func (c Client) RulesList() (string, error) {
	panic("unimplemented")
}

// RollupRead returns the rollup of kind covering at (now when zero) in the
// session's stores, with its children.
func (c Client) RollupRead(kind WindowKind, at time.Time) (string, error) {
	panic("unimplemented")
}

// Index is what a session here starts with.
func (c Client) Index() (string, error) {
	panic("unimplemented")
}

// Edit applies a person's edit to a page in store ("" for the session's
// project, or global without one). It is the CLI's rules edit and wiki edit.
func (c Client) Edit(store string, e Edit) (Change, error) {
	panic("unimplemented")
}

// History returns a page's changes, newest first.
func (c Client) History(store string, ref PageRef) ([]Change, error) {
	panic("unimplemented")
}

// EntryFilter narrows spin log.
type EntryFilter struct {
	Store   string // "" for every store
	Session string // prefix
	Last    int    // 0: all
}

// StoreEntry is a log entry and the store it is in.
type StoreEntry struct {
	Store string
	Entry
}

// Entries returns log entries across stores, oldest first, filtered.
func (c Client) Entries(f EntryFilter) ([]StoreEntry, error) {
	panic("unimplemented")
}
