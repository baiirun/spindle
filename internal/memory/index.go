package memory

import (
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
// Ensures: every rule topic appears, in full while RulesBytes lasts and by
// slug after.
// Ensures: each timeline is fitted by FitTimeline within its share; a share
// one section doesn't use passes to the other.
func RenderIndex(in IndexInput) string {
	panic("unimplemented")
}

// TimelineLine is one line FitTimeline picked: a node's id and line, or a
// note that older history was left out.
type TimelineLine struct {
	ID   string // "" for the note
	At   time.Time
	Text string
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
	panic("unimplemented")
}

// Index loads the views for p's session context and renders them. It is
// what the SessionStart hook returns.
func (p Project) Index(now time.Time) (string, error) {
	panic("unimplemented")
}
