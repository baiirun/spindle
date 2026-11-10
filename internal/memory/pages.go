package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	n := NameOf(strings.TrimSuffix(strings.TrimSpace(s), ".md"))
	if n == "" {
		return "", fmt.Errorf("%q isn't a page name: use lowercase words joined by hyphens", s)
	}
	return Slug(n), nil
}

// PageRef names a page within a store.
type PageRef struct {
	Layer Layer `json:"layer"`
	Slug  Slug  `json:"slug"`
}

// ParsePageRef reads "rules/<slug>" or "wiki/<slug>".
func ParsePageRef(s string) (PageRef, error) {
	layer, name, ok := strings.Cut(s, "/")
	if !ok || (Layer(layer) != RulesLayer && Layer(layer) != WikiLayer) {
		return PageRef{}, fmt.Errorf("%q isn't a page: want rules/<name> or wiki/<name>", s)
	}
	slug, err := ParseSlug(name)
	return PageRef{Layer: Layer(layer), Slug: slug}, err
}

func (r PageRef) String() string { return string(r.Layer) + "/" + string(r.Slug) }

// EntityKind is what a wiki page is about.
type EntityKind string

// EntityKinds are every kind, in the order prompts list them.
var EntityKinds = []EntityKind{KindPerson, KindProject, KindLink, KindTool, KindConcept, KindOrg}

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
	page, fields, err := parsePage(slug, text)
	if err != nil {
		return Rule{}, err
	}
	if _, ok := fields["kind"]; ok {
		return Rule{}, fmt.Errorf("rules/%s: a rule has no kind; is it a wiki page?", slug)
	}
	return Rule{page}, nil
}

func ParseWikiPage(slug Slug, text string) (WikiPage, error) {
	page, fields, err := parsePage(slug, text)
	if err != nil {
		return WikiPage{}, err
	}
	kind := EntityKind(fields["kind"])
	if !slices.Contains(EntityKinds, kind) {
		return WikiPage{}, fmt.Errorf("wiki/%s: kind %q, want one of %v", slug, kind, EntityKinds)
	}
	return WikiPage{Page: page, Kind: kind}, nil
}

// Render is a page's file text; Parse(Render(x)) == x.
func (r Rule) Render() string { return r.render("") }

func (w WikiPage) Render() string { return w.render(w.Kind) }

// The frontmatter is a few "key: value" lines between --- fences. It is
// spindle's own small format, not YAML, so it parses strictly.
var pageKeys = []string{"name", "title", "kind", "evidence", "updated"}

func (p Page) render(kind EntityKind) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s\ntitle: %s\n", p.Slug, oneLine(p.Title))
	if kind != "" {
		fmt.Fprintf(&b, "kind: %s\n", kind)
	}
	if len(p.Evidence) > 0 {
		ids := make([]string, len(p.Evidence))
		for i, id := range p.Evidence {
			ids[i] = string(id)
		}
		fmt.Fprintf(&b, "evidence: %s\n", strings.Join(ids, ", "))
	}
	if !p.Updated.IsZero() {
		fmt.Fprintf(&b, "updated: %s\n", p.Updated.UTC().Format(time.RFC3339))
	}
	b.WriteString("---\n\n")
	b.WriteString(p.Body)
	return b.String()
}

func parsePage(slug Slug, text string) (Page, map[string]string, error) {
	fail := func(format string, args ...any) (Page, map[string]string, error) {
		return Page{}, nil, fmt.Errorf("%s: %s", slug, fmt.Sprintf(format, args...))
	}
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return fail("no frontmatter: the file must start with a --- line")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return fail("frontmatter has no closing --- line")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(head, "\n") {
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || !slices.Contains(pageKeys, key) {
			return fail("frontmatter line %q: want one of %v as key: value", line, pageKeys)
		}
		fields[key] = strings.TrimSpace(value)
	}
	if fields["name"] != string(slug) {
		return fail("name %q doesn't match the file's", fields["name"])
	}
	p := Page{Slug: slug, Title: fields["title"], Body: strings.TrimPrefix(body, "\n")}
	if p.Title == "" {
		return fail("no title")
	}
	for _, id := range strings.Split(fields["evidence"], ",") {
		if id = strings.TrimSpace(id); id != "" {
			p.Evidence = append(p.Evidence, EntryID(id))
		}
	}
	if u := fields["updated"]; u != "" {
		t, err := time.Parse(time.RFC3339, u)
		if err != nil {
			return fail("updated %q isn't an RFC 3339 time", u)
		}
		p.Updated = t
	}
	return p, fields, nil
}

// CheckPageText parses text as a page of ref's layer, the check every write
// goes through. The CLI's editor uses it to catch a typo before saving.
func CheckPageText(ref PageRef, text string) error {
	var err error
	if ref.Layer == RulesLayer {
		_, err = ParseRule(ref.Slug, text)
	} else {
		_, err = ParseWikiPage(ref.Slug, text)
	}
	return err
}

// Rules reads the store's rule topics, by slug. A file that doesn't parse
// is left out and named in the error; the rest are still returned, so one
// bad hand edit doesn't hide every rule.
func (p Project) Rules() ([]Rule, error) {
	return loadPages(p, RulesLayer, ParseRule)
}

// Wiki reads the store's wiki pages, by slug, as Rules does.
func (p Project) Wiki() ([]WikiPage, error) {
	return loadPages(p, WikiLayer, ParseWikiPage)
}

func loadPages[T any](p Project, layer Layer, parse func(Slug, string) (T, error)) ([]T, error) {
	if p.Name == "" {
		return nil, nil
	}
	paths, err := filepath.Glob(filepath.Join(p.Dir, string(layer), "*.md"))
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	var out []T
	var errs []error
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		page, err := parse(Slug(strings.TrimSuffix(filepath.Base(path), ".md")), string(b))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s/%s/%w", p.Name, layer, err))
			continue
		}
		out = append(out, page)
	}
	return out, errors.Join(errs...)
}

func (p Project) pagePath(ref PageRef) string {
	return filepath.Join(p.Dir, string(ref.Layer), string(ref.Slug)+".md")
}

// PageText reads one page's file text, "" if it doesn't exist.
func (p Project) PageText(ref PageRef) (string, error) {
	if p.Name == "" {
		return "", nil
	}
	b, err := os.ReadFile(p.pagePath(ref))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
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
//
// The whole batch is checked before anything is written, so a bad edit
// writes nothing.
func (p Project) ApplyEdits(edits []Edit, by Actor, now time.Time) ([]Change, error) {
	for _, e := range edits {
		if strings.TrimSpace(e.Reason) == "" {
			return nil, fmt.Errorf("%s/%s: an edit needs a reason", p.Name, e.Target)
		}
		if e.After == "" {
			continue
		}
		if err := CheckPageText(e.Target, e.After); err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
	}
	if err := p.Init(); err != nil {
		return nil, err
	}
	unlock, err := p.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []Change
	for _, e := range edits {
		before, err := p.PageText(e.Target)
		if err != nil {
			return out, err
		}
		if before == e.After {
			continue
		}
		path := p.pagePath(e.Target)
		if e.After == "" {
			err = os.Remove(path)
		} else {
			err = writeFile(path, []byte(e.After))
		}
		if err != nil {
			return out, err
		}
		c := Change{ID: "c" + strings.TrimPrefix(string(NewEntryID(now)), "e"), At: now.UTC(), By: by, Target: e.Target,
			Before: before, After: e.After, Evidence: e.Evidence, Reason: oneLine(e.Reason)}
		if err := appendJSONL(p.changesPath(), []Change{c}); err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (p Project) changesPath() string { return filepath.Join(p.Dir, "page-changes.jsonl") }

// Changes reads page-changes.jsonl, oldest first.
func (p Project) Changes() ([]Change, error) {
	if p.Name == "" {
		return nil, nil
	}
	return readJSONL[Change](p.changesPath())
}
