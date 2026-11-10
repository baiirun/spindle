package memory

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	began := time.Now()
	ctx := c.Context()
	msg, err := func() (string, error) {
		g, err := ParseGist(gist)
		if err != nil {
			return "", err
		}
		st := ctx
		switch scope {
		case ScopeGlobal:
			st = Store(c.Home, GlobalName)
		case ScopeProject, "":
			if ctx.Name == "" {
				st = Store(c.Home, UnfiledName)
			}
		default:
			return "", fmt.Errorf("scope %q: want project or global", scope)
		}
		kind := FromAgent
		if c.Caller == "cli" {
			kind = FromCLI
		}
		e := Entry{ID: NewEntryID(began), At: began.UTC(), Folder: c.Cwd, Source: Pointer{Kind: kind, Note: oneLine(note)}, Gist: g}
		if err := st.Append([]Entry{e}); err != nil {
			return "", err
		}
		if st.IsUnfiled() {
			return fmt.Sprintf("Noted as %s, in unfiled until this folder is linked to a project.", e.ID), nil
		}
		return fmt.Sprintf("Noted in %s as %s.", st.Name, e.ID), nil
	}()
	c.log(ctx, "append", nil, gist, "", began, err)
	return msg, err
}

// corpus loads every store. What couldn't be read is named after the
// answer, so a damaged store doesn't hide the rest.
func (c Client) corpus() (Corpus, string) {
	corp, err := LoadCorpus(c.Home)
	if err != nil {
		return corp, "\n(Some memory couldn't be read: " + errString(err) + ")\n"
	}
	return corp, ""
}

// Search searches every store; see Search.
func (c Client) Search(query string, layers []SearchLayer, limit int) (string, error) {
	began := time.Now()
	for _, l := range layers {
		if !slices.Contains([]SearchLayer{SearchRules, SearchWiki, SearchRollups, SearchLog}, l) {
			return "", fmt.Errorf("layer %q: want rules, wiki, rollups or log", l)
		}
	}
	corp, warn := c.corpus()
	hits := Search(corp, query, layers, limit)
	c.log(c.Context(), "search", nil, query, fmt.Sprintf("%d hits", len(hits)), began, nil)
	return FormatHits(hits) + warn, nil
}

// Zoom opens an id from the index; see Zoom.
func (c Client) Zoom(id string) (string, error) {
	began := time.Now()
	corp, warn := c.corpus()
	out, err := Zoom(corp, strings.TrimSpace(id))
	c.log(c.Context(), "zoom", []string{id}, "", "", began, err)
	return out + warn, err
}

// WikiRead returns one wiki page with its cited entries. A slug several
// stores have opens in each.
func (c Client) WikiRead(slug string) (string, error) {
	s, err := ParseSlug(slug)
	if err != nil {
		return "", err
	}
	return c.Zoom("wiki/" + string(s))
}

// sessionStores are the stores a session here loads: its project's, if it
// has one, then global.
func (c Client) sessionStores() []Project {
	ctx := c.Context()
	g := Store(c.Home, GlobalName)
	if ctx.Name == "" || ctx.IsGlobal() {
		return []Project{g}
	}
	return []Project{ctx, g}
}

// RulesList returns the rule topics a session here loads: global's and the
// session's project's, in full.
func (c Client) RulesList() (string, error) {
	began := time.Now()
	var b strings.Builder
	var errs []error
	for _, st := range c.sessionStores() {
		rules, err := st.Rules()
		errs = append(errs, err)
		fmt.Fprintf(&b, "## %s\n", st.Name)
		if len(rules) == 0 {
			b.WriteString("No rules.\n")
		}
		for _, r := range rules {
			fmt.Fprintf(&b, "### %s (rules/%s)\n%s\n", r.Title, r.Slug, strings.TrimSpace(r.Body))
		}
		b.WriteString("\n")
	}
	err := errors.Join(errs...)
	c.log(c.Context(), "rules", nil, "", "", began, err)
	if err != nil {
		b.WriteString("(Some rules couldn't be read: " + errString(err) + ")\n")
	}
	return b.String(), nil
}

// RollupRead returns the rollup of kind covering at (now when zero) in the
// session's stores, with its children.
//
// A session window is the session that covers at, or else the last one
// before it.
func (c Client) RollupRead(kind WindowKind, at time.Time) (string, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var b strings.Builder
	for _, st := range c.sessionStores() {
		tl, err := st.timeline()
		if err != nil {
			return "", err
		}
		id := ""
		switch kind {
		case DayWindow:
			id = Window{DayWindow, dayKey(at.In(time.Local))}.String()
		case WeekWindow:
			id = Window{WeekWindow, weekKey(at.In(time.Local))}.String()
		case SessionWindow:
			id = sessionAt(tl, at)
		default:
			return "", fmt.Errorf("window %q: want session, day or week", kind)
		}
		if n, ok := findNode(tl, id); ok && id != "" {
			fmt.Fprintf(&b, "## %s\n%s\n", st.Name, zoomStore(StoreCorpus{Name: st.Name, Timeline: tl}, n.ID))
		}
	}
	if b.Len() == 0 {
		return "Nothing was noted then.\n", nil
	}
	return b.String(), nil
}

// sessionAt is the id of the session covering at, else the last one that
// ended before it, else "".
func sessionAt(tl []Node, at time.Time) string {
	var covering, before string
	var beforeEnd time.Time
	var walk func(ns []Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			if !strings.HasPrefix(n.ID, string(SessionWindow)+":") {
				walk(n.Children)
				continue
			}
			first, last := n.Children[0].At, n.At
			switch {
			case !at.Before(first) && !at.After(last):
				covering = n.ID
			case last.Before(at) && last.After(beforeEnd):
				before, beforeEnd = n.ID, last
			}
		}
	}
	walk(tl)
	return cmp.Or(covering, before)
}

func (c Client) Index() (string, error) {
	return c.Context().Index(time.Now())
}

// store resolves a store name for the CLI: "" is the session's project, or
// global when the folder has none.
func (c Client) store(name string) (Project, error) {
	if name == "" {
		if ctx := c.Context(); ctx.Name != "" {
			return ctx, nil
		}
		return Store(c.Home, GlobalName), nil
	}
	st := Store(c.Home, name)
	if !st.Exists() {
		return st, fmt.Errorf("no store %q", name)
	}
	return st, nil
}

// Edit applies a person's edit to a page in store ("" for the session's
// project, or global without one). It is the CLI's rules edit and wiki edit.
//
// Ensures: an error if the edit changes nothing, so the editor's caller can
// say so instead of reporting a change.
func (c Client) Edit(store string, e Edit) (Change, error) {
	began := time.Now()
	st, err := c.store(store)
	if err != nil {
		return Change{}, err
	}
	changes, err := st.ApplyEdits([]Edit{e}, ByCLI, began)
	if err == nil && len(changes) == 0 {
		err = errNoChange
	}
	c.log(st, "edit", []string{st.Name + "/" + e.Target.String()}, "", e.Reason, began, err)
	if err != nil {
		return Change{}, err
	}
	return changes[0], nil
}

var errNoChange = errors.New("nothing changed")

// IsNoChange reports whether err is Edit's report that the page was left as it was.
func IsNoChange(err error) bool { return errors.Is(err, errNoChange) }

// History returns a page's changes, newest first.
func (c Client) History(store string, ref PageRef) ([]Change, error) {
	st, err := c.store(store)
	if err != nil {
		return nil, err
	}
	all, err := st.Changes()
	var out []Change
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Target == ref {
			out = append(out, all[i])
		}
	}
	return out, err
}

// FormatHistory lists a page's changes, newest first; with full set it
// includes the text each replaced.
func FormatHistory(ref PageRef, hist []Change, full bool) string {
	if len(hist) == 0 {
		return ref.String() + " has no recorded changes.\n"
	}
	var b strings.Builder
	for i, c := range hist {
		what := "edited"
		switch {
		case c.Before == "":
			what = "created"
		case c.After == "":
			what = "retired"
		}
		fmt.Fprintf(&b, "%d. %s %s by %s: %s\n", i+1, c.At.Local().Format("2006-01-02 15:04"), what, c.By, c.Reason)
		if len(c.Evidence) > 0 {
			ids := make([]string, len(c.Evidence))
			for j, id := range c.Evidence {
				ids[j] = string(id)
			}
			fmt.Fprintf(&b, "   evidence: %s\n", strings.Join(ids, ", "))
		}
		if full && c.Before != "" {
			b.WriteString("   before:\n" + indent(c.Before, "     ") + "\n")
		}
	}
	return b.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
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
	all, err := stores(c.Home)
	if err != nil {
		return nil, err
	}
	all = append(all, Store(c.Home, UnfiledName))
	var out []StoreEntry
	for _, st := range all {
		if f.Store != "" && st.Name != NameOf(f.Store) {
			continue
		}
		es, err := st.Entries()
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if strings.HasPrefix(e.Session, f.Session) {
				out = append(out, StoreEntry{Store: st.Name, Entry: e})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b StoreEntry) int { return a.At.Compare(b.At) })
	if f.Last > 0 && len(out) > f.Last {
		out = out[len(out)-f.Last:]
	}
	return out, nil
}

// String renders an entry as one line for spin log.
func (e StoreEntry) String() string {
	return fmt.Sprintf("%s  %-12s %s  %s", e.At.Local().Format("01-02 15:04"), clipRunes(e.Store, 12), e.ID, e.Gist)
}

// Store is the store a CLI command names: "" for the session's project, or
// global when the folder has none.
func (c Client) Store(name string) (Project, error) { return c.store(name) }
