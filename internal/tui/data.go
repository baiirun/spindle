package tui

import (
	"sort"
	"strings"

	"spindle/internal/memory"
)

// The viewer reads the stores directly rather than through memory.Client:
// every Client call goes to the activity log, and browsing would fill it with
// reads nobody made in a session.

// Snapshot is everything the viewer shows, read from the stores at once.
type Snapshot struct {
	Groups []Group        // global first, then the projects by name
	Events []memory.Event // the activity log, oldest first
}

// Group is one store and its memories, newest modified first.
type Group struct {
	Name        string
	Description string
	Context     bool // the folder's project
	Memories    []memory.Memory
}

// Item is one memory and the store it is in. Its Memory.Name is bare.
type Item struct {
	Project string
	Memory  memory.Memory
}

// Ref is how the memory is named everywhere: <project>/<name>.
func (it Item) Ref() string { return it.Project + "/" + it.Memory.Name }

// Load reads global, every project and the activity log. context names the
// project to mark as the folder's ("" for none).
func Load(home, context string) (Snapshot, error) {
	ps, err := memory.Projects(home)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	for _, p := range append([]memory.Project{memory.Store(home, memory.GlobalName)}, ps...) {
		mems, err := p.Load()
		if err != nil {
			return Snapshot{}, err
		}
		desc := p.Meta().Description
		if p.IsGlobal() {
			desc = "loaded in every project"
		}
		s.Groups = append(s.Groups, Group{Name: p.Name, Description: desc, Context: p.Name == context, Memories: mems})
	}
	if s.Events, err = (memory.Project{Home: home}).Events(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Items lists the memories of group i.
func (s Snapshot) Items(i int) []Item {
	if i < 0 || i >= len(s.Groups) {
		return nil
	}
	g := s.Groups[i]
	out := make([]Item, len(g.Memories))
	for j, m := range g.Memories {
		out[j] = Item{Project: g.Name, Memory: m}
	}
	return out
}

// Group returns the index of the named group, or -1.
func (s Snapshot) Group(name string) int {
	for i, g := range s.Groups {
		if g.Name == name {
			return i
		}
	}
	return -1
}

// Search ranks the memories of every store by the query's words, as spin
// search does. An empty query lists every memory, newest first.
func (s Snapshot) Search(query string, limit int) []Item {
	var all []Item
	for i := range s.Groups {
		all = append(all, s.Items(i)...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Memory.Modified.After(all[j].Memory.Modified) })
	if strings.TrimSpace(query) == "" {
		return all
	}
	byRef := map[string]Item{}
	mems := make([]memory.Memory, len(all))
	for i, it := range all {
		byRef[it.Ref()] = it
		mems[i] = it.Memory
		mems[i].Name = it.Ref()
	}
	var out []Item
	for _, m := range memory.Search(mems, query, limit) {
		out = append(out, byRef[m.Name])
	}
	return out
}

// History is the memory's changes, newest first, as spin history lists them.
func History(home string, it Item) ([]memory.Change, error) {
	all, err := memory.Store(home, it.Project).Changes()
	var out []memory.Change
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Name == it.Memory.Name {
			out = append(out, all[i])
		}
	}
	return out, err
}

// Activity is the events that name the memory, newest first.
func (s Snapshot) Activity(it Item) []memory.Event {
	var out []memory.Event
	for i := len(s.Events) - 1; i >= 0; i-- {
		e := s.Events[i]
		for _, f := range e.Files {
			if names(f, e.Project, it) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// names reports whether f, a file in an event of the session's project
// sessionProject, is the item. Files come as "project/name.md" from recalls
// and tool calls, "+project/name" or "-project/name" from extraction and
// dreams, and as a bare name from older logs, where it meant the session's
// project.
func names(f, sessionProject string, it Item) bool {
	f = strings.TrimSuffix(strings.TrimLeft(strings.TrimSpace(f), "+-"), ".md")
	if store, name, ok := strings.Cut(f, "/"); ok {
		return store == it.Project && name == it.Memory.Name
	}
	return f == it.Memory.Name && sessionProject == it.Project
}

// Recalls counts the events that put the memory into a session's context.
func Recalls(evs []memory.Event) int {
	n := 0
	for _, e := range evs {
		if e.Kind == "recall" {
			n++
		}
	}
	return n
}
