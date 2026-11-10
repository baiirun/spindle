package memory

import (
	"time"
)

// Rules and wiki pages are Markdown files with a small frontmatter. Both are
// edited by dream and by hand, and every edit goes through ApplyEdits, which
// records it in page-changes.jsonl.
//
// Granularity: a rule file is a topic ("git-and-prs", "running-commands")
// holding the directives about it as a bulleted list, not one directive per
// file. Dream's prompt asks for that; nothing enforces it.

// Layer is one of the two page layers.
type Layer string

const (
	RulesLayer Layer = "rules"
	WikiLayer  Layer = "wiki"
)

// Slug is a kebab-case page name of 1 to 64 characters, [a-z0-9-], also its
// file stem. Only ParseSlug makes one.
type Slug string

// ParseSlug normalizes s (lowercase, runs of other characters become one
// hyphen, a trailing .md dropped) and returns an error when nothing is left.
func ParseSlug(s string) (Slug, error) {
	panic("unimplemented")
}

// PageRef names a page within a store.
type PageRef struct {
	Layer Layer `json:"layer"`
	Slug  Slug  `json:"slug"`
}

// ParsePageRef reads "rules/<slug>" or "wiki/<slug>".
func ParsePageRef(s string) (PageRef, error) {
	panic("unimplemented")
}

func (r PageRef) String() string { return string(r.Layer) + "/" + string(r.Slug) }

// EntityKind is what a wiki page is about.
type EntityKind string

const (
	KindPerson  EntityKind = "person"
	KindProject EntityKind = "project"
	KindLink    EntityKind = "link"
	KindTool    EntityKind = "tool"
	KindConcept EntityKind = "concept"
	KindOrg     EntityKind = "org"
)

// Page is what rules and wiki pages share.
type Page struct {
	Slug     Slug
	Title    string
	Body     string    // Markdown, after the frontmatter
	Evidence []EntryID // the log entries it rests on, in this store
	Updated  time.Time
}

// Rule is one rule topic.
type Rule struct{ Page }

// WikiPage is one entity's page.
type WikiPage struct {
	Page
	Kind EntityKind
}

// ParseRule and ParseWikiPage read a page file. They are the only way text
// becomes a page, from disk, from dream or from an editor, so a page that
// loads is always well formed.
//
// Ensures: an error for missing or invalid frontmatter (title; kind for wiki,
// one of the EntityKinds), or a slug that doesn't match the file's.
func ParseRule(slug Slug, text string) (Rule, error) {
	panic("unimplemented")
}

func ParseWikiPage(slug Slug, text string) (WikiPage, error) {
	panic("unimplemented")
}

// Render is a page's file text; Parse(Render(x)) == x.
func (r Rule) Render() string {
	panic("unimplemented")
}

func (w WikiPage) Render() string {
	panic("unimplemented")
}

// Rules reads the store's rule topics, by slug.
func (p Project) Rules() ([]Rule, error) {
	panic("unimplemented")
}

// Wiki reads the store's wiki pages, by slug.
func (p Project) Wiki() ([]WikiPage, error) {
	panic("unimplemented")
}

// PageText reads one page's file text, "" if it doesn't exist.
func (p Project) PageText(ref PageRef) (string, error) {
	panic("unimplemented")
}

// Actor is who made a page change.
type Actor string

const (
	ByDream     Actor = "dream"
	ByCLI       Actor = "cli" // a person, through spin rules edit or spin wiki edit
	ByMigration Actor = "migration"
)

// Edit is a requested page change. After is the page's whole new file text;
// "" retires the page.
type Edit struct {
	Target   PageRef   `json:"target"`
	After    string    `json:"after"`
	Evidence []EntryID `json:"evidence"`
	Reason   string    `json:"reason"`
}

// Change is one line of page-changes.jsonl: an applied Edit with the text it
// replaced. Before "" is a creation; After "" is a retirement.
type Change struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	By       Actor     `json:"by"`
	Target   PageRef   `json:"target"`
	Before   string    `json:"before,omitempty"`
	After    string    `json:"after,omitempty"`
	Evidence []EntryID `json:"evidence,omitempty"`
	Reason   string    `json:"reason"`
}

// ApplyEdits writes edits to the store's pages under its lock, in order.
//
// Requires: every edit has a Reason, and its After (when not "") parses as a
// page of its layer.
// Ensures: an edit whose After equals the current text is skipped and
// recorded nowhere, so rerunning a batch after a crash converges.
// Ensures: each page is written (atomically) before its Change is appended;
// a crash between the two loses that one record, never the page.
func (p Project) ApplyEdits(edits []Edit, by Actor, now time.Time) ([]Change, error) {
	panic("unimplemented")
}

// Changes reads page-changes.jsonl, oldest first.
func (p Project) Changes() ([]Change, error) {
	panic("unimplemented")
}
