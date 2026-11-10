// Package tui is spin tui, a read-only viewer: stores on the left, a store's
// rules, wiki pages and log entries in the middle, and the selected item's
// text, change history or activity on the right.
package tui

import (
	"fmt"
	"strings"
	"time"

	"spindle/internal/memory"
)

// The viewer reads the stores directly rather than through memory.Client:
// every Client call goes to the activity log, and browsing would fill it with
// reads nobody made in a session.

// Snapshot is everything the viewer shows, read from the stores at once.
type Snapshot struct {
	Stores []Store        // global first, then the projects by name, then unfiled
	Events []memory.Event // the activity log, oldest first
	corpus memory.Corpus
}

// Store is one store's items.
type Store struct {
	Name        string
	Description string
	Context     bool   // the folder's project
	Items       []Item // rules, then wiki pages, then log entries newest first
}

// Item is one rule topic, wiki page or log entry.
type Item struct {
	Store string
	ID    string // rules/<slug>, wiki/<slug> or an entry id
	Tag   string // rule, the wiki page's kind, or entry
	Title string
	At    time.Time
	Body  string
	Page  *memory.PageRef // nil for an entry
}

// Ref is how the item is named everywhere: <store>/<id>.
func (it Item) Ref() string { return it.Store + "/" + it.ID }

// Load reads every store and the activity log. context names the project to
// mark as the folder's ("" for none). A store that can't be read whole shows
// what could be read, and the error says what couldn't.
func Load(home, context string) (Snapshot, error) {
	corp, err := memory.LoadCorpus(home)
	s := Snapshot{corpus: corp}
	for _, sc := range corp.Stores {
		st := Store{Name: sc.Name, Context: sc.Name == context, Description: memory.Store(home, sc.Name).Meta().Description}
		switch sc.Name {
		case memory.GlobalName:
			st.Description = "loaded in every project"
		case memory.UnfiledName:
			st.Description = "noted in folders with no project"
		}
		st.Items = items(sc)
		s.Stores = append(s.Stores, st)
	}
	evs, evErr := (memory.Project{Home: home}).Events()
	s.Events = evs
	if err == nil {
		err = evErr
	}
	return s, err
}

func items(sc memory.StoreCorpus) []Item {
	byID := map[memory.EntryID]memory.Entry{}
	for _, e := range sc.Entries {
		byID[e.ID] = e
	}
	cited := func(ids []memory.EntryID) string {
		if len(ids) == 0 {
			return ""
		}
		var b strings.Builder
		b.WriteString("\n\nRests on:\n")
		for _, id := range ids {
			if e, ok := byID[id]; ok {
				fmt.Fprintf(&b, "- %s %s %s\n", e.ID, e.At.Local().Format("2006-01-02"), e.Gist)
			}
		}
		return b.String()
	}
	var out []Item
	for _, r := range sc.Rules {
		ref := memory.PageRef{Layer: memory.RulesLayer, Slug: r.Slug}
		out = append(out, Item{Store: sc.Name, ID: ref.String(), Tag: "rule", Title: r.Title, At: r.Updated, Body: r.Body + cited(r.Evidence), Page: &ref})
	}
	for _, w := range sc.Wiki {
		ref := memory.PageRef{Layer: memory.WikiLayer, Slug: w.Slug}
		out = append(out, Item{Store: sc.Name, ID: ref.String(), Tag: string(w.Kind), Title: w.Title, At: w.Updated, Body: w.Body + cited(w.Evidence), Page: &ref})
	}
	for i := len(sc.Entries) - 1; i >= 0; i-- {
		e := sc.Entries[i]
		body, _ := memory.Zoom(memory.Corpus{Stores: []memory.StoreCorpus{sc}}, string(e.ID))
		out = append(out, Item{Store: sc.Name, ID: string(e.ID), Tag: "entry", Title: string(e.Gist), At: e.At, Body: body})
	}
	return out
}

// Group returns the index of the named store, or -1.
func (s Snapshot) Group(name string) int {
	for i, g := range s.Stores {
		if g.Name == name {
			return i
		}
	}
	return -1
}

// Items lists the items of store i.
func (s Snapshot) Items(i int) []Item {
	if i < 0 || i >= len(s.Stores) {
		return nil
	}
	return s.Stores[i].Items
}

// Search ranks every store's items by the query's words, as spin search
// does. An empty query lists every rule and page.
func (s Snapshot) Search(query string, limit int) []Item {
	byRef := map[string]Item{}
	var pages []Item
	for _, st := range s.Stores {
		for _, it := range st.Items {
			byRef[it.Ref()] = it
			if it.Page != nil {
				pages = append(pages, it)
			}
		}
	}
	if strings.TrimSpace(query) == "" {
		return pages
	}
	layers := []memory.SearchLayer{memory.SearchRules, memory.SearchWiki, memory.SearchLog}
	var out []Item
	for _, h := range memory.Search(s.corpus, query, layers, limit) {
		if it, ok := byRef[h.Store+"/"+h.ID]; ok {
			out = append(out, it)
		}
	}
	return out
}

// History is a page's changes, newest first, as spin rules history and spin
// wiki history list them. An entry has none: the log never changes.
func History(home string, it Item) ([]memory.Change, error) {
	if it.Page == nil {
		return nil, nil
	}
	all, err := memory.Store(home, it.Store).Changes()
	var out []memory.Change
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Target == *it.Page {
			out = append(out, all[i])
		}
	}
	return out, err
}

// Activity is the events that name the item, newest first. Dreams name
// pages as rules/<slug>; CLI edits as <store>/rules/<slug>.
func (s Snapshot) Activity(it Item) []memory.Event {
	var out []memory.Event
	for i := len(s.Events) - 1; i >= 0; i-- {
		e := s.Events[i]
		for _, f := range e.Files {
			if f == it.Ref() || (f == it.ID && e.Project == it.Store) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}
