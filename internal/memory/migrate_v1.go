package memory

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// MigrateV1 turns each store's spindle v1 memories into the layers, next to
// the v1 files, which it only reads:
//
//	every v1 changes.jsonl row  -> a log entry at its time ("extract wrote x: <description>")
//	a memory file v1 never logged -> a log entry for its current text
//	user, directive memories    -> one rule topic each
//	project memories            -> a wiki page of kind project
//	reference memories          -> a wiki page of kind link
//	any other type              -> a wiki page of kind concept
//
// Each page cites the entry of its memory's last logged write, or its own
// entry when v1 never logged one (a file written by hand). Rule topics are migrated one per
// memory; dream merges them into topics over time.

// v1Memory is one v1 memory file.
type v1Memory struct {
	Name        string
	Type        string
	Description string
	Body        string
	Modified    time.Time
	Path        string
}

// v1Change is one row of a v1 store's changes.jsonl.
type v1Change struct {
	At      time.Time `json:"at"`
	Op      string    `json:"op"` // write or delete
	Name    string    `json:"name"`
	By      string    `json:"by"`
	Session string    `json:"session,omitempty"`
	Old     string    `json:"old,omitempty"`
	New     string    `json:"new,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

// V1Report says what migrating one store did.
type V1Report struct {
	Store   string
	Entries int      // entries appended
	Rules   int      // rule topics created
	Pages   int      // wiki pages created
	Skip    string   // why the store was passed over: "no v1 memories"
	Errors  []string // files that didn't parse, by path; the rest still migrate
}

// MigrateV1 migrates every store that has v1 memories.
//
// Ensures: v1 files are never written, moved or deleted.
// Ensures: it converges. Entry ids are derived from the v1 file or change
// row they come from, so a rerun appends only what is missing, and pages
// are created only where missing. A rerun after a crash finishes the job; a
// rerun after success adds what v1 wrote since, and nothing else.
// Ensures: dream starts after the migrated entries when they are all the log
// holds, since the migrated pages already reflect them.
// Must not: overwrite a page that exists, which dream or a person may have
// edited since.
func MigrateV1(home string, now time.Time) ([]V1Report, error) {
	all, err := stores(home)
	if err != nil {
		return nil, err
	}
	var reports []V1Report
	for _, st := range all {
		r, err := migrateStore(st, now)
		reports = append(reports, r)
		if err != nil {
			return reports, fmt.Errorf("migrate %s: %w", st.Name, err)
		}
	}
	return reports, nil
}

func migrateStore(st Project, now time.Time) (V1Report, error) {
	r := V1Report{Store: st.Name}
	mems, bad, err := readV1Memories(filepath.Join(st.Dir, "memory"))
	if err != nil {
		return r, err
	}
	r.Errors = bad
	if len(mems) == 0 {
		r.Skip = "no v1 memories"
		return r, nil
	}
	changesPath := filepath.Join(st.Dir, "changes.jsonl")
	changes, err := readJSONL[v1Change](changesPath)
	if err != nil {
		return r, err
	}
	entries, edits := planV1(st.Name, changesPath, mems, changes, now)

	have, err := st.Entries()
	if err != nil {
		return r, err
	}
	known := map[EntryID]bool{}
	for _, e := range have {
		known[e.ID] = true
	}
	var missing []Entry
	for _, e := range entries {
		if !known[e.ID] {
			missing = append(missing, e)
		}
	}
	// Entries first: a page must never cite an entry the log doesn't have.
	if err := st.Append(missing); err != nil {
		return r, err
	}
	r.Entries = len(missing)

	var create []Edit
	for _, e := range edits {
		text, err := st.PageText(e.Target)
		if err != nil {
			return r, err
		}
		if text == "" {
			create = append(create, e)
		}
	}
	changed, err := st.ApplyEdits(create, ByMigration, now)
	if err != nil {
		return r, err
	}
	if err := skipMigratedForDream(st, now); err != nil {
		return r, err
	}
	for _, c := range changed {
		if c.Target.Layer == RulesLayer {
			r.Rules++
		} else {
			r.Pages++
		}
	}
	st.Log(Event{At: now, Kind: "migrate", Project: st.Name, Note: fmt.Sprintf("%d entries, %d rules, %d pages", r.Entries, r.Rules, r.Pages)})
	return r, nil
}

// skipMigratedForDream moves a store's dream checkpoint past its log when the
// log holds only v1 entries and dream never ran: the migrated pages already
// say what those entries do, and dreaming them would cost many passes to
// learn nothing. A log with anything else in it is left for dream to read.
func skipMigratedForDream(st Project, now time.Time) error {
	cp, err := st.LoadCheckpoint()
	if err != nil || cp.Read > 0 {
		return err
	}
	es, err := st.Entries()
	if err != nil || len(es) == 0 {
		return err
	}
	for _, e := range es {
		if e.Source.Kind != FromV1 {
			return nil
		}
	}
	return writeJSON(st.checkpointPath(), Checkpoint{Read: len(es), Last: es[len(es)-1].ID, At: now.UTC()})
}

// planV1 maps one store's v1 memories and changes to entries and page edits.
// It is the whole mapping, and pure.
//
// Ensures: one entry per change row, and one per memory with no write row;
// one edit per memory, citing its last write or its own entry; slugs are the
// v1 names.
// Ensures: the same input gives the same ids.
func planV1(store, changesPath string, mems []v1Memory, changes []v1Change, now time.Time) ([]Entry, []Edit) {
	var entries []Entry
	lastWrite := map[string]EntryID{} // memory name -> its latest write's entry
	for i, c := range changes {
		line := i + 1
		e := Entry{
			ID:      EntryID("e-v1-" + digest(store, "change", fmt.Sprint(line), c.At.UTC().Format(time.RFC3339Nano))),
			At:      c.At.UTC(),
			Session: c.Session,
			Source:  Pointer{Kind: FromV1, Path: changesPath, Line: line},
			Gist:    v1Gist(changeGist(c)),
		}
		entries = append(entries, e)
		if c.Op == "write" {
			lastWrite[c.Name] = e.ID
		}
	}
	slices.SortStableFunc(entries, func(a, b Entry) int { return a.At.Compare(b.At) })

	mems = slices.Clone(mems)
	slices.SortStableFunc(mems, func(a, b v1Memory) int { return a.Modified.Compare(b.Modified) })
	var edits []Edit
	for _, m := range mems {
		evidence, ok := lastWrite[m.Name]
		if !ok {
			e := Entry{
				ID:     EntryID("e-v1-" + digest(store, "memory", m.Path, m.Modified.UTC().Format(time.RFC3339Nano))),
				At:     m.Modified.UTC(),
				Source: Pointer{Kind: FromV1, Path: m.Path},
				Gist:   v1Gist(fmt.Sprintf("v1 %s memory %s: %s", orDefault(m.Type, "untyped"), m.Name, m.Description)),
			}
			entries = append(entries, e)
			evidence = e.ID
		}
		slug, err := ParseSlug(m.Name)
		if err != nil {
			continue
		}
		body := m.Body + "\n"
		if d := oneLine(m.Description); d != "" {
			body = d + "\n\n" + body
		}
		page := Page{Slug: slug, Title: titleOf(m.Name), Body: body, Evidence: []EntryID{evidence}, Updated: m.Modified.UTC()}
		edit := Edit{Evidence: page.Evidence, Reason: "migrated from spindle v1 " + m.Path}
		switch m.Type {
		case "user", "directive":
			edit.Target, edit.After = PageRef{RulesLayer, slug}, Rule{page}.Render()
		default:
			kind := map[string]EntityKind{"project": KindProject, "reference": KindLink}[m.Type]
			edit.Target, edit.After = PageRef{WikiLayer, slug}, WikiPage{Page: page, Kind: cmp.Or(kind, KindConcept)}.Render()
		}
		edits = append(edits, edit)
	}
	return entries, edits
}

// titleOf makes a page title from a v1 name: "no-attribution" -> "No attribution".
func titleOf(name string) string {
	t := strings.ReplaceAll(strings.TrimSpace(name), "-", " ")
	if t == "" {
		return name
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

func changeGist(c v1Change) string {
	if c.Op == "delete" {
		return fmt.Sprintf("v1 %s deleted %s: %s", orDefault(c.By, "someone"), c.Name, orDefault(c.Reason, "no reason given"))
	}
	desc := ""
	if m, err := parseV1(c.New); err == nil {
		desc = m.Description
	}
	return fmt.Sprintf("v1 %s wrote %s: %s", orDefault(c.By, "someone"), c.Name, desc)
}

// v1Gist fits migrated text into a gist. Migration is the one writer that
// clips: the text is v1's, and the full version stays in the v1 file the
// entry points at.
func v1Gist(s string) Gist {
	s = oneLine(s)
	if len(s) > GistBytes {
		s = clipBytes(s, GistBytes-len("…")) + "…"
	}
	g, err := ParseGist(s)
	if err != nil {
		panic(fmt.Sprintf("v1 gist %q: %v", s, err)) // every v1 line names its memory, so it's never empty
	}
	return g
}

func readV1Memories(dir string) ([]v1Memory, []string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, nil, err
	}
	var mems []v1Memory
	var bad []string
	for _, path := range paths {
		if filepath.Base(path) == "MEMORY.md" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		m, err := parseV1(string(b))
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		m.Name = orDefault(m.Name, strings.TrimSuffix(filepath.Base(path), ".md"))
		m.Modified, m.Path = info.ModTime(), path
		mems = append(mems, m)
	}
	return mems, bad, nil
}

// parseV1 reads a v1 memory file as v1 did: a flat "type:" works as well as
// metadata.type, and unknown keys are ignored.
func parseV1(text string) (v1Memory, error) {
	var m v1Memory
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return m, errors.New("no frontmatter")
	}
	head, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return m, errors.New("unterminated frontmatter")
	}
	for _, line := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "name":
			m.Name = v
		case "description":
			m.Description = v
		case "type":
			m.Type = v
		}
	}
	m.Body = strings.TrimSpace(body)
	return m, nil
}
