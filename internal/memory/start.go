package memory

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// StartBudget keeps session-start context under Claude Code's 10,000
// character cap on hook output and about Codex's default 2,500 tokens.
const StartBudget = 9_500

// Shares of the start budget left after the instructions, in percent. Global
// comes first because its rules apply everywhere; a section that needs less
// than its share passes the rest on, so a section only runs short when the
// others need their whole share too.
const (
	GlobalShare  = 20
	ProjectShare = 65
	OthersShare  = 15
)

// StartContext is what a session gets at start, resume, clear and after
// compaction: the memory instructions and the indexes, the counterpart of
// Claude Code loading its memory section and MEMORY.md. p is the session's
// context. The sections are global, the session's project, and a line per
// other project. Each one is cut on its own, never into the next.
func (p Project) StartContext() string {
	secs := p.startSections()
	fits := fitShares(secs, startRoom())
	var b strings.Builder
	b.WriteString(Instructions + "\n\n")
	for i, s := range secs {
		b.WriteString(s.show(fits[i]))
	}
	return b.String()
}

// startRoom is what the start budget leaves for the indexes.
func startRoom() int { return StartBudget - utf8.RuneCountInString(Instructions) - 2 }

// GlobalRoom is global's share of the start context, which its dream keeps it
// within.
func GlobalRoom() int { return startRoom() * GlobalShare / 100 }

// indexText is every section in full, for memory_index and spin index,
// which aren't bound by the start budget.
func (p Project) indexText() string {
	var b strings.Builder
	for _, s := range p.startSections() {
		b.WriteString(s.show(s.items))
	}
	return b.String()
}

// startSection is one section of the start context. show(k) renders it
// with its first k items in keep order, ending with a line for the rest.
type startSection struct {
	items int
	show  func(k int) string
	share int
}

func (s startSection) cost(k int) int { return utf8.RuneCountInString(s.show(k)) }

// fitShares says how many items of each section fit in room. Each section
// first gets up to its share; whatever is left over then goes, in order, to
// the sections that still have items to show.
func fitShares(secs []startSection, room int) []int {
	fits := make([]int, len(secs))
	used := 0
	for i, s := range secs {
		fits[i] = fitItems(s, room*s.share/100)
		used += s.cost(fits[i])
	}
	for i, s := range secs {
		if n := fitItems(s, room-used+s.cost(fits[i])); n > fits[i] {
			used += s.cost(n) - s.cost(fits[i])
			fits[i] = n
		}
	}
	return fits
}

// fitItems is the most items of s that fit in n characters. The heading and
// the line for what was cut always show, even when they alone run over.
func fitItems(s startSection, n int) int {
	if s.cost(s.items) <= n {
		return s.items
	}
	// Below all items, cost grows with k: each item adds a line, and the
	// cut line only ever gets a digit shorter.
	return sort.Search(s.items, func(k int) bool { return s.cost(k+1) > n })
}

func (p Project) startSections() []startSection {
	g := Store(p.Home, GlobalName)
	secs := []startSection{indexSection(g, "# global (every project)\n\n", GlobalShare)}
	switch {
	case p.Name != "" && !p.IsGlobal():
		head := fmt.Sprintf("# %s (this folder's project)\n\n", p.Name)
		if d := p.Meta().Description; d != "" {
			head += d + "\n\n"
		}
		secs = append(secs, indexSection(p, head, ProjectShare))
	case p.Name == "":
		head := fmt.Sprintf("# No project for this folder\n\n%s isn't linked to a project. To save something about this work, link it to a related project below or to a new one with memory_project (link_this_folder), or ask the user if it isn't clear which. Until then, name the project on each write.\n\n", orDash(p.Cwd))
		secs = append(secs, startSection{show: func(int) string { return head }, share: ProjectShare})
	}
	return append(secs, p.othersSection())
}

// indexSection is a store's index. When it runs over its room it keeps the
// directives, then the most recently changed memories; the rest are still
// found by recall and memory_search.
func indexSection(s Project, head string, share int) startSection {
	mems, _ := s.Load() // newest first
	sort.SliceStable(mems, func(i, j int) bool {
		return mems[i].Type == "directive" && mems[j].Type != "directive"
	})
	return startSection{items: len(mems), share: share, show: func(k int) string {
		if len(mems) == 0 {
			return head + "No memories yet.\n\n"
		}
		text := head + strings.TrimSpace(refIndex(s, Index(mems[:k]))) + "\n\n"
		if cut := len(mems) - k; cut > 0 {
			text += fmt.Sprintf("+%d more in %s, use memory_search\n\n", cut, s.Name)
		}
		return text
	}}
}

// othersSection is a line per other project, most recently changed first.
func (p Project) othersSection() startSection {
	type other struct {
		line   string
		latest int64
	}
	var os []other
	ps, _ := Projects(p.Home)
	for _, o := range ps {
		if o.Name == p.Name {
			continue
		}
		mems, _ := o.Load()
		x := other{line: fmt.Sprintf("- %s (%d memories)", o.Name, len(mems))}
		if d := o.Meta().Description; d != "" {
			x.line += ": " + clipRunes(d, 140)
		}
		if len(mems) > 0 {
			x.latest = mems[0].Modified.UnixNano()
		}
		os = append(os, x)
	}
	sort.SliceStable(os, func(i, j int) bool { return os[i].latest > os[j].latest })
	return startSection{items: len(os), share: OthersShare, show: func(k int) string {
		if len(os) == 0 {
			return ""
		}
		text := "# Other projects\n\nRecall and memory_search cover these too.\n\n"
		for _, o := range os[:k] {
			text += o.line + "\n"
		}
		if cut := len(os) - k; cut > 0 {
			text += fmt.Sprintf("+%d more projects, use memory_projects\n", cut)
		}
		return text
	}}
}
