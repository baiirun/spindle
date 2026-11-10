package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The index is what a session starts with, after OptMem's wake: the
// instructions, the rules in full, each store's timeline with detail fading
// with age, the wiki's page names, and a line per other project. Every
// timeline line carries an id that memory_zoom opens.

// IndexBytes caps the whole index. It is fixed: every session pays for it.
const IndexBytes = 12_000

// Section caps within IndexBytes. Rules are loaded before acting, so they
// come first and get a fixed share; the timelines share what's left after
// the instructions, rules, wiki names and other projects.
const (
	RulesBytes   = 4_000 // past this, the remaining topics are listed by slug only
	WikiBytes    = 1_200 // page slugs and titles
	OthersBytes  = 800   // one line per other project
	GlobalShare  = 30    // percent of the timeline room for global's timeline
	ProjectShare = 70    // and for the session's project
)

// StoreView is one store as the index shows it.
type StoreView struct {
	Name        string
	Description string
	Rules       []Rule
	Timeline    []Node
	Wiki        []WikiPage
}

// IndexInput is everything the index shows. Project is nil when the folder
// has no project.
type IndexInput struct {
	Global  StoreView
	Project *StoreView
	Unfiled int // with no project: unfiled entries from this folder, so the index can offer to link it
	Others  []ProjectInfo
	Now     time.Time
}

// RenderIndex lays out the index.
//
// Ensures: at most IndexBytes bytes, for any input.
// Ensures: every rule topic appears, in full while RulesBytes lasts, then by
// slug while that room lasts, then counted.
// Ensures: each timeline is fitted by FitTimeline within its share; a share
// one section doesn't use passes to the other.
func RenderIndex(in IndexInput) string {
	views := []StoreView{in.Global}
	if in.Project != nil {
		views = []StoreView{*in.Project, in.Global}
	}
	var head strings.Builder
	head.WriteString(Instructions + "\n")
	head.WriteString(rulesSection(views))
	if in.Project == nil {
		head.WriteString("\nThis folder isn't linked to a project, so what you note about the work here waits in unfiled. ")
		if in.Unfiled > 0 {
			fmt.Fprintf(&head, "%d notes from this folder are waiting. ", in.Unfiled)
		}
		head.WriteString("Link it to a project with memory_project when it's clear which one, or ask the user.\n")
	}
	tail := wikiSection(views) + othersSection(in.Others)

	titles := make([]string, len(views))
	room := IndexBytes - head.Len() - len(tail)
	for i, v := range views {
		titles[i] = fmt.Sprintf("\n## Timeline: %s (oldest first; memory_zoom <id> opens a line)\n", v.Name)
		room -= len(titles[i])
	}
	fitted := fitShares(views, max(room, 0))

	var b strings.Builder
	b.WriteString(head.String())
	for i := range views {
		if len(views[i].Timeline) == 0 {
			continue
		}
		b.WriteString(titles[i])
		for _, l := range fitted[i] {
			b.WriteString(l.String())
		}
	}
	b.WriteString(tail)
	out := b.String()
	if len(out) > IndexBytes {
		panic(fmt.Sprintf("index is %d bytes, over %d: a section ignored its budget", len(out), IndexBytes))
	}
	return out
}

// fitShares fits each view's timeline in its share of room: the project's
// first, then global with what the project left, then the project again
// with what global left.
func fitShares(views []StoreView, room int) [][]TimelineLine {
	out := make([][]TimelineLine, len(views))
	if len(views) == 1 {
		out[0] = FitTimeline(views[0].Timeline, room)
		return out
	}
	projectRoom := room * ProjectShare / 100
	out[0] = FitTimeline(views[0].Timeline, projectRoom)
	globalRoom := room - linesSize(out[0])
	out[1] = FitTimeline(views[1].Timeline, globalRoom)
	out[0] = FitTimeline(views[0].Timeline, room-linesSize(out[1]))
	return out
}

func rulesSection(views []StoreView) string {
	var b strings.Builder
	b.WriteString("\n## Rules\n")
	n := 0
	var rest []string
	for _, v := range views {
		for _, r := range v.Rules {
			n++
			text := fmt.Sprintf("### %s (%s rules/%s)\n%s\n", r.Title, v.Name, r.Slug, strings.TrimSpace(r.Body))
			if rest == nil && b.Len()+len(text) <= RulesBytes {
				b.WriteString(text)
				continue
			}
			rest = append(rest, v.Name+" rules/"+string(r.Slug))
		}
	}
	if n == 0 {
		return "\n## Rules\nNone yet.\n"
	}
	if len(rest) > 0 {
		b.WriteString(listWithin("More rules (rules_list shows them in full): ", rest, RulesBytes-b.Len()))
	}
	return b.String()
}

func wikiSection(views []StoreView) string {
	var items []string
	for _, v := range views {
		for _, w := range v.Wiki {
			items = append(items, fmt.Sprintf("%s (%s, %s)", w.Slug, w.Kind, v.Name))
		}
	}
	if len(items) == 0 {
		return ""
	}
	const head = "\n## Wiki\n"
	return head + listWithin("Pages (wiki_read <slug>): ", items, WikiBytes-len(head))
}

func othersSection(others []ProjectInfo) string {
	if len(others) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Other projects\n")
	for i, o := range others {
		line := fmt.Sprintf("- %s: %s (%d entries)\n", o.Name, clipRunes(orDash(o.Description), 80), o.Entries)
		more := fmt.Sprintf("+%d more (memory_projects)\n", len(others)-i)
		if b.Len()+len(line)+len(more) > OthersBytes && i < len(others)-1 {
			b.WriteString(more)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// listWithin joins items after prefix in at most room bytes, counting the
// ones that don't fit.
func listWithin(prefix string, items []string, room int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i, it := range items {
		sep := ", "
		if i == 0 {
			sep = ""
		}
		more := fmt.Sprintf(" +%d more", len(items)-i)
		if b.Len()+len(sep)+len(it)+len(more)+1 > room {
			b.WriteString(more)
			break
		}
		b.WriteString(sep + it)
	}
	b.WriteString("\n")
	if b.Len() > room {
		return ""
	}
	return b.String()
}

// TimelineLine is one line FitTimeline picked: a node's id and line, or a
// note that older history was left out.
type TimelineLine struct {
	ID   string // "" for the note
	At   time.Time
	Text string
}

// String is the line as the index prints it; its length is what budgets count.
func (l TimelineLine) String() string {
	if l.ID == "" {
		return l.Text + "\n"
	}
	return l.ID + " " + l.Text + "\n"
}

func linesSize(ls []TimelineLine) int {
	n := 0
	for _, l := range ls {
		n += len(l.String())
	}
	return n
}

// FitTimeline picks the lines of one timeline that fit in budgetBytes, oldest
// first, with detail fading with age. It starts from the weeks and opens the
// newest node it can afford into its children, again and again, so recent
// entries are shown word for word and old ones as week lines.
//
// Ensures: a node without a Line is always shown as its children, never
// skipped; if even that doesn't fit, the oldest lines go and a note says how
// many entries they held and to use memory_zoom.
// Ensures: the result never exceeds budgetBytes, and every entry is covered
// by at most one line.
func FitTimeline(timeline []Node, budgetBytes int) []TimelineLine {
	// The frontier is the nodes currently shown, oldest first. A node with no
	// line can only be shown as its children, so those open first.
	var frontier []Node
	var open func(n Node)
	open = func(n Node) {
		if n.Line == "" && len(n.Children) > 0 {
			for _, c := range n.Children {
				open(c)
			}
			return
		}
		frontier = append(frontier, n)
	}
	for _, n := range timeline {
		open(n)
	}
	size := 0
	for _, n := range frontier {
		size += nodeCost(n)
	}
	// Open the newest node that has children while its parts fit. Stopping
	// at the first that doesn't keeps older detail coarser than newer.
	for i := len(frontier) - 1; i >= 0; i-- {
		n := frontier[i]
		if len(n.Children) == 0 {
			continue
		}
		saved := frontier
		frontier = nil
		for _, c := range n.Children {
			open(c)
		}
		parts := frontier
		delta := -nodeCost(n)
		for _, c := range parts {
			delta += nodeCost(c)
		}
		if size+delta > budgetBytes {
			frontier = saved
			break
		}
		size += delta
		frontier = append(append(append([]Node{}, saved[:i]...), parts...), saved[i+1:]...)
		i += len(parts) // look again from the newest of the parts
	}
	// Over budget even fully collapsed (or with missing rollups): drop the
	// oldest lines and say how many entries they held.
	dropped := 0
	for len(frontier) > 0 && size > budgetBytes {
		dropped += leaves(frontier[0])
		size -= nodeCost(frontier[0])
		frontier = frontier[1:]
	}
	out := make([]TimelineLine, 0, len(frontier)+1)
	if dropped > 0 {
		note := TimelineLine{Text: fmt.Sprintf("+%d older entries not shown: memory_zoom an id or memory_search to reach them", dropped)}
		for len(frontier) > 0 && size+len(note.String()) > budgetBytes {
			dropped += leaves(frontier[0])
			size -= nodeCost(frontier[0])
			frontier = frontier[1:]
			note.Text = fmt.Sprintf("+%d older entries not shown: memory_zoom an id or memory_search to reach them", dropped)
		}
		if size+len(note.String()) <= budgetBytes {
			out = append(out, note)
		}
	}
	for _, n := range frontier {
		out = append(out, timelineLine(n))
	}
	return out
}

func timelineLine(n Node) TimelineLine {
	text := n.Line
	if len(n.Children) == 0 { // an entry: its id carries no date
		text = n.At.Local().Format("01-02 15:04") + " " + n.Line
	}
	return TimelineLine{ID: n.ID, At: n.At, Text: text}
}

func nodeCost(n Node) int { return len(timelineLine(n).String()) }

func leaves(n Node) int {
	if len(n.Children) == 0 {
		return 1
	}
	c := 0
	for _, k := range n.Children {
		c += leaves(k)
	}
	return c
}

// Index loads the views for p's session context and renders them. It is
// what the SessionStart hook returns.
//
// A store that can't be read whole (a damaged log, a page that doesn't
// parse) still shows what can be read; the problem goes to the activity log
// and one line of the index, since a session must always start.
func (p Project) Index(now time.Time) (string, error) {
	var problems []error
	view := func(s Project) StoreView {
		v, err := s.view()
		if err != nil {
			problems = append(problems, err)
		}
		return v
	}
	in := IndexInput{Global: view(Store(p.Home, GlobalName)), Now: now}
	if p.Name != "" && !p.IsGlobal() {
		v := view(p)
		in.Project = &v
	} else if p.Cwd != "" {
		es, err := Store(p.Home, UnfiledName).Entries()
		if err != nil {
			problems = append(problems, err)
		}
		for _, e := range es {
			if e.Folder != "" && within(e.Folder, p.Cwd) {
				in.Unfiled++
			}
		}
	}
	all, err := p.ProjectList()
	if err != nil {
		problems = append(problems, err)
	}
	for _, o := range all {
		if !o.Context && o.Name != GlobalName {
			in.Others = append(in.Others, o)
		}
	}
	text := RenderIndex(in)
	if err := errors.Join(problems...); err != nil {
		p.Log(Event{At: now, Kind: "warn", Note: "index: " + errString(err)})
		if warn := "\nSome memory couldn't be read; spin activity shows why.\n"; len(text)+len(warn) <= IndexBytes {
			text += warn
		}
	}
	return text, nil
}

// view reads what the index shows of one store. It returns what it could
// read alongside any error.
func (p Project) view() (StoreView, error) {
	v := StoreView{Name: p.Name, Description: p.Meta().Description}
	var errs []error
	var err error
	if v.Rules, err = p.Rules(); err != nil {
		errs = append(errs, err)
	}
	if v.Wiki, err = p.Wiki(); err != nil {
		errs = append(errs, err)
	}
	if v.Timeline, err = p.timeline(); err != nil {
		errs = append(errs, err)
	}
	return v, errors.Join(errs...)
}

// timeline is the store's timeline in the machine's time zone.
func (p Project) timeline() ([]Node, error) {
	es, err := p.Entries()
	if err != nil {
		return nil, err
	}
	rs, err := p.Rollups()
	return Timeline(es, rs, time.Local), err
}
