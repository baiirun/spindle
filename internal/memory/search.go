package memory

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Search and zoom read every store; a folder's project only decides what a
// session starts with, never what it can find.

// SearchLayer is a layer memory_search can look in.
type SearchLayer string

const (
	SearchRules   SearchLayer = "rules"
	SearchWiki    SearchLayer = "wiki"
	SearchRollups SearchLayer = "rollups"
	SearchLog     SearchLayer = "log"
)

// Hit is one search result. Evidence holds the log entries behind it, so an
// answer built on it can be checked: a page's cited entries, a rollup's
// entries, or the entry itself.
type Hit struct {
	Store    string
	Layer    SearchLayer
	ID       string // a page ref, window id or entry id
	Line     string // a page's title, a rollup's line, an entry's gist
	Evidence []Entry
}

// Corpus is what search looks through: every store, loaded once.
type Corpus struct {
	Stores []StoreCorpus
}

// StoreCorpus is one store's layers.
type StoreCorpus struct {
	Name     string
	Entries  []Entry
	Rollups  []Rollup
	Rules    []Rule
	Wiki     []WikiPage
	Timeline []Node
}

// LoadCorpus reads every store.
//
// It returns what it could read alongside an error naming what it couldn't,
// so one damaged store doesn't hide the others.
func LoadCorpus(home string) (Corpus, error) {
	all, err := stores(home)
	all = append(all, Store(home, UnfiledName))
	errs := []error{err}
	var c Corpus
	for _, st := range all {
		sc := StoreCorpus{Name: st.Name}
		var e error
		if sc.Entries, e = st.Entries(); e != nil {
			errs = append(errs, e)
		}
		if sc.Rollups, e = st.Rollups(); e != nil {
			errs = append(errs, e)
		}
		if sc.Rules, e = st.Rules(); e != nil {
			errs = append(errs, e)
		}
		if sc.Wiki, e = st.Wiki(); e != nil {
			errs = append(errs, e)
		}
		sc.Timeline = Timeline(sc.Entries, sc.Rollups, time.Local)
		c.Stores = append(c.Stores, sc)
	}
	return c, errors.Join(errs...)
}

// Search ranks the corpus's items in layers (all four when empty) by how
// many query words they share, then by recency, and returns the first limit.
//
// Ensures: a page's hit carries at most 5 evidence entries, newest first.
func Search(c Corpus, query string, layers []SearchLayer, limit int) []Hit {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	in := func(l SearchLayer) bool { return len(layers) == 0 || slices.Contains(layers, l) }
	type scored struct {
		hit   Hit
		score int
		at    time.Time
	}
	var all []scored
	add := func(h Hit, text string, at time.Time) {
		text = strings.ToLower(text)
		n := 0
		for _, w := range words {
			if strings.Contains(text, w) {
				n++
			}
		}
		if n > 0 {
			all = append(all, scored{h, n, at})
		}
	}
	adopted := map[EntryID]bool{} // unfiled entries a project has copied: search shows the copy
	for _, sc := range c.Stores {
		for _, e := range sc.Entries {
			if e.Adopted != "" {
				adopted[e.Adopted] = true
			}
		}
	}
	for _, sc := range c.Stores {
		byID := map[EntryID]Entry{}
		for _, e := range sc.Entries {
			byID[e.ID] = e
		}
		cited := func(ids []EntryID) []Entry {
			var out []Entry
			for _, id := range ids {
				if e, ok := byID[id]; ok {
					out = append(out, e)
				}
			}
			return newest(out, 5)
		}
		if in(SearchRules) {
			for _, r := range sc.Rules {
				add(Hit{Store: sc.Name, Layer: SearchRules, ID: "rules/" + string(r.Slug), Line: r.Title, Evidence: cited(r.Evidence)}, r.Title+" "+r.Body, r.Updated)
			}
		}
		if in(SearchWiki) {
			for _, w := range sc.Wiki {
				add(Hit{Store: sc.Name, Layer: SearchWiki, ID: "wiki/" + string(w.Slug), Line: w.Title, Evidence: cited(w.Evidence)}, string(w.Slug)+" "+w.Title+" "+w.Body, w.Updated)
			}
		}
		if in(SearchRollups) {
			for _, r := range sc.Rollups {
				var under []Entry
				if n, ok := findNode(sc.Timeline, r.Window.String()); ok {
					for _, id := range leafIDs(n) {
						under = append(under, byID[EntryID(id)])
					}
				}
				add(Hit{Store: sc.Name, Layer: SearchRollups, ID: r.Window.String(), Line: string(r.Line), Evidence: newest(under, 5)}, string(r.Line), r.Built)
			}
		}
		if in(SearchLog) {
			for _, e := range sc.Entries {
				if sc.Name == UnfiledName && adopted[e.ID] {
					continue
				}
				add(Hit{Store: sc.Name, Layer: SearchLog, ID: string(e.ID), Line: string(e.Gist), Evidence: []Entry{e}}, string(e.Gist), e.At)
			}
		}
	}
	slices.SortStableFunc(all, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.score, a.score), b.at.Compare(a.at))
	})
	out := make([]Hit, 0, min(limit, len(all)))
	for _, s := range all[:min(limit, len(all))] {
		out = append(out, s.hit)
	}
	return out
}

// newest returns at most n entries, newest first.
func newest(es []Entry, n int) []Entry {
	es = slices.Clone(es)
	slices.SortFunc(es, func(a, b Entry) int { return b.At.Compare(a.At) })
	return es[:min(n, len(es))]
}

func findNode(ns []Node, id string) (Node, bool) {
	for _, n := range ns {
		if n.ID == id {
			return n, true
		}
		if found, ok := findNode(n.Children, id); ok {
			return found, true
		}
	}
	return Node{}, false
}

func leafIDs(n Node) []string {
	if len(n.Children) == 0 {
		return []string{n.ID}
	}
	var out []string
	for _, c := range n.Children {
		out = append(out, leafIDs(c)...)
	}
	return out
}

// FormatHits renders search results for both front ends.
func FormatHits(hits []Hit) string {
	if len(hits) == 0 {
		return "Nothing matches.\n"
	}
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "%s %s: %s\n", h.Store, h.ID, h.Line)
		if h.Layer == SearchLog {
			fmt.Fprintf(&b, "  %s\n", formatPointer(h.Evidence[0]))
			continue
		}
		for _, e := range h.Evidence {
			fmt.Fprintf(&b, "  %s %s %s\n", e.ID, e.At.Local().Format("2006-01-02"), e.Gist)
		}
	}
	return b.String()
}

// formatPointer says where an entry was said, for a person to go and check.
func formatPointer(e Entry) string {
	src := string(e.Source.Kind)
	if e.Source.Path != "" {
		src += " " + e.Source.Path
		if e.Source.Line > 0 {
			src += fmt.Sprintf("#L%d", e.Source.Line)
		}
	}
	if e.Source.Note != "" {
		src += " (" + e.Source.Note + ")"
	}
	return "from " + src
}

// Zoom opens one id from the index: a window into its rollup line and its
// children's lines, an entry into its gist and pointer, or a page ref into
// the page.
//
// An id may be prefixed with its store ("spindle/d:2026-11-08"); without the
// prefix, an id several stores have (a day, a slug) opens in each, headed by
// the store's name.
//
// Ensures: an error naming the id when no store has it.
func Zoom(c Corpus, id string) (string, error) {
	stores := c.Stores
	if name, rest, ok := strings.Cut(id, "/"); ok && rest != "" {
		for _, sc := range c.Stores {
			if sc.Name == name {
				stores, id = []StoreCorpus{sc}, rest
			}
		}
	}
	var b strings.Builder
	for _, sc := range stores {
		if text := zoomStore(sc, id); text != "" {
			if len(stores) > 1 || len(c.Stores) > 1 {
				fmt.Fprintf(&b, "## %s\n", sc.Name)
			}
			b.WriteString(text + "\n")
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("no store has %q: ids look like w:2026-W45, d:2026-11-08, s:<session>, e-..., rules/<slug> or wiki/<slug>", id)
	}
	return b.String(), nil
}

func zoomStore(sc StoreCorpus, id string) string {
	byID := map[EntryID]Entry{}
	for _, e := range sc.Entries {
		byID[e.ID] = e
	}
	if e, ok := byID[EntryID(id)]; ok {
		return formatEntry(e)
	}
	if ref, err := ParsePageRef(id); err == nil {
		var text string
		var evidence []EntryID
		for _, r := range sc.Rules {
			if ref.Layer == RulesLayer && r.Slug == ref.Slug {
				text, evidence = r.Render(), r.Evidence
			}
		}
		for _, w := range sc.Wiki {
			if ref.Layer == WikiLayer && w.Slug == ref.Slug {
				text, evidence = w.Render(), w.Evidence
			}
		}
		if text == "" {
			return ""
		}
		var b strings.Builder
		b.WriteString(text)
		if len(evidence) > 0 {
			b.WriteString("\nRests on:\n")
			for _, eid := range evidence {
				if e, ok := byID[eid]; ok {
					fmt.Fprintf(&b, "- %s %s %s\n", e.ID, e.At.Local().Format("2006-01-02"), e.Gist)
				}
			}
		}
		return b.String()
	}
	n, ok := findNode(sc.Timeline, id)
	if !ok || len(n.Children) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", n.ID, orDefault(n.Line, "(not summarized yet)"))
	for _, c := range n.Children {
		b.WriteString("  " + timelineLine(c).String())
	}
	return b.String()
}

func formatEntry(e Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n%s\n%s\n", e.ID, e.At.Local().Format("2006-01-02 15:04"), e.Gist, formatPointer(e))
	if e.Session != "" {
		fmt.Fprintf(&b, "session %s\n", e.Session)
	}
	if e.Folder != "" {
		fmt.Fprintf(&b, "folder %s\n", e.Folder)
	}
	if e.Adopted != "" {
		fmt.Fprintf(&b, "adopted from unfiled %s\n", e.Adopted)
	}
	return b.String()
}
