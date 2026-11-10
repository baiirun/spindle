package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spindle/internal/model"
	"spindle/internal/source"
)

// Dream is one agent session over one store: a model with tools reads the
// log entries since the store's checkpoint, the pages, the transcripts the
// entries point at and the project's repo, and edits rules and wiki pages
// through `spin dream write` and `spin dream retire`. Each of those checks
// one edit and records it, so a bad edit is refused and the agent can fix it.

// The gate: a store is due when this many distinct sessions have added
// entries since its checkpoint and its last dream is this old.
const (
	DreamMinSessions = 5
	DreamEvery       = 24 * time.Hour
	// DreamMaxEntries caps the entries one pass reads, oldest first; the
	// checkpoint moves only past those, and the next pass reads on.
	DreamMaxEntries = 300
)

// Checkpoint is dream.json: how far dream has read the store's log. Only
// ApplyDream writes it, under the store's dream.lock.
type Checkpoint struct {
	Read int       `json:"read"` // entries read, a prefix of the log
	Last EntryID   `json:"last"` // the id of entry Read-1, to catch a log that changed under it
	At   time.Time `json:"at"`   // when that dream ran; zero for never
}

// DreamDue reports whether the gate is open.
//
// Requires: cp.Read <= len(entries).
func DreamDue(cp Checkpoint, entries []Entry, now time.Time) bool {
	if !cp.At.IsZero() && now.Sub(cp.At) < DreamEvery {
		return false
	}
	sessions := map[string]bool{}
	for _, e := range entries[cp.Read:] {
		if e.Session != "" {
			sessions[e.Session] = true
		}
	}
	return len(sessions) >= DreamMinSessions
}

func (p Project) checkpointPath() string { return filepath.Join(p.Dir, "dream.json") }

// LoadCheckpoint reads dream.json; a store that never dreamed has the zero one.
func (p Project) LoadCheckpoint() (Checkpoint, error) {
	var cp Checkpoint
	if p.Name == "" {
		return cp, nil
	}
	return cp, readJSON(p.checkpointPath(), &cp)
}

// DreamInput is what a dream starts from, and what each dream command checks
// an edit against.
type DreamInput struct {
	Store   string // the store's name, for the prompt
	Fresh   []Entry
	Rollups []Rollup // those over the days the fresh entries fall in
	Rules   []Rule
	Wiki    []WikiPage
	Folders []string // the project's linked folders, for the agent to read
	Through Checkpoint
}

// LoadDreamInput gathers a store's DreamInput.
//
// Ensures: an error if the checkpoint's Last isn't the id at Read-1, since
// the log then isn't the one dream read.
//
// A page that doesn't parse is an error here, unlike at session start:
// dream would otherwise take it for missing and overwrite the hand edit.
func (p Project) LoadDreamInput(now time.Time) (DreamInput, error) {
	in := DreamInput{Store: p.Name}
	cp, err := p.LoadCheckpoint()
	if err != nil {
		return in, err
	}
	es, err := p.Entries()
	if err != nil {
		return in, err
	}
	if cp.Read > len(es) || (cp.Read > 0 && es[cp.Read-1].ID != cp.Last) {
		return in, fmt.Errorf("%s: dream.json says %d entries were read, ending at %s, but the log doesn't match; move dream.json aside to dream from the start", p.Name, cp.Read, cp.Last)
	}
	read := min(len(es), cp.Read+DreamMaxEntries)
	in.Fresh = es[cp.Read:read]
	in.Through = Checkpoint{Read: read, At: now.UTC()}
	if read > 0 {
		in.Through.Last = es[read-1].ID
	}
	if in.Rules, err = p.Rules(); err != nil {
		return in, err
	}
	if in.Wiki, err = p.Wiki(); err != nil {
		return in, err
	}
	rollups, err := p.Rollups()
	if err != nil {
		return in, err
	}
	spans := map[Window]bool{}
	for _, e := range in.Fresh {
		spans[Window{DayWindow, dayKey(e.At.In(time.Local))}] = true
		if e.Session != "" {
			spans[Window{SessionWindow, e.Session}] = true
		}
	}
	for _, r := range rollups {
		if spans[r.Window] {
			in.Rollups = append(in.Rollups, r)
		}
	}
	in.Folders = p.Meta().Folders
	return in, nil
}

// DreamEdit is one edit as a dream command takes it: fields, not file text,
// so code renders the frontmatter and keeps the page's evidence.
type DreamEdit struct {
	Target   string
	Title    string
	Kind     EntityKind
	Body     string
	Evidence []EntryID
	Reason   string
	Retire   bool
}

func (pe DreamEdit) edit(in DreamInput, now time.Time) (Edit, error) {
	e := Edit{Evidence: pe.Evidence, Reason: oneLine(pe.Reason)}
	ref, err := ParsePageRef(pe.Target)
	if err != nil {
		return e, err
	}
	e.Target = ref
	if pe.Retire {
		return e, nil
	}
	page := Page{Slug: ref.Slug, Title: oneLine(pe.Title), Body: strings.TrimSpace(pe.Body) + "\n", Updated: now}
	page.Evidence = mergeIDs(in.cited(ref), pe.Evidence)
	if ref.Layer == RulesLayer {
		e.After = Rule{page}.Render()
	} else {
		e.After = WikiPage{Page: page, Kind: pe.Kind}.Render()
	}
	return e, nil
}

func mergeIDs(a, b []EntryID) []EntryID {
	out := slices.Clone(a)
	for _, id := range b {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// cited is the evidence the page ref already has, nil if it doesn't exist.
func (in DreamInput) cited(ref PageRef) []EntryID {
	p, ok := in.page(ref)
	if !ok {
		return nil
	}
	return p.Evidence
}

func (in DreamInput) page(ref PageRef) (Page, bool) {
	if ref.Layer == RulesLayer {
		for _, r := range in.Rules {
			if r.Slug == ref.Slug {
				return r.Page, true
			}
		}
		return Page{}, false
	}
	for _, w := range in.Wiki {
		if w.Slug == ref.Slug {
			return w.Page, true
		}
	}
	return Page{}, false
}

// An edit is an upsert. It is rejected when its Reason is empty; its After
// doesn't parse as a page of its layer; it retires a page that doesn't exist;
// or it cites an entry, in the edit or in its page, that is neither in
// in.Fresh nor already cited by the page.
func CheckEdits(in DreamInput, edits []Edit) (ok []Edit, rejected []Rejection) {
	known := map[EntryID]bool{}
	for _, e := range in.Fresh {
		known[e.ID] = true
	}
	for _, e := range edits {
		if why := checkEdit(in, known, e); why != "" {
			rejected = append(rejected, Rejection{Edit: e, Why: why})
			continue
		}
		ok = append(ok, e)
	}
	return ok, rejected
}

func checkEdit(in DreamInput, fresh map[EntryID]bool, e Edit) string {
	if strings.TrimSpace(e.Reason) == "" {
		return "no reason"
	}
	old, exists := in.page(e.Target)
	if e.After == "" && !exists {
		return "retires " + e.Target.String() + ", which doesn't exist"
	}
	cites := e.Evidence
	if e.After != "" {
		if err := CheckPageText(e.Target, e.After); err != nil {
			return err.Error()
		}
		page, _, _ := parsePage(e.Target.Slug, e.After)
		cites = mergeIDs(cites, page.Evidence)
	}
	for _, id := range cites {
		if !fresh[id] && !slices.Contains(old.Evidence, id) {
			return fmt.Sprintf("cites %s, which this pass wasn't shown", id)
		}
	}
	return ""
}

// Rejection is an edit a dream command refused, and why.
type Rejection struct {
	Edit Edit
	Why  string
}

// DreamMode says whether a dream's commands write or only record what they
// would write.
type DreamMode string

const (
	DreamApply  DreamMode = "apply"
	DreamDryRun DreamMode = "dry-run"
)

// dreamSession is dream-session.json, written by Dream for its commands: it
// says which store they edit, which entries they may cite, and the mode.
type dreamSession struct {
	Home    string    `json:"home"`
	Store   string    `json:"store"`
	Fresh   []EntryID `json:"fresh"`
	Mode    DreamMode `json:"mode"`
	Started time.Time `json:"started"`
}

// dreamOutcome is one line of dream-session.jsonl: what one command did.
type dreamOutcome struct {
	Edit     Edit   `json:"edit"`
	Change   string `json:"change,omitempty"`   // the change it recorded
	Rejected string `json:"rejected,omitempty"` // why it refused
	Planned  bool   `json:"planned,omitempty"`  // a dry run's edit
}

func (p Project) dreamSessionPath() string  { return filepath.Join(p.Dir, "dream-session.json") }
func (p Project) dreamOutcomesPath() string { return filepath.Join(p.Dir, "dream-session.jsonl") }

// DreamResult is what a dream did.
type DreamResult struct {
	Fresh      int      // entries it read
	Summary    string   // the agent's last message
	Changes    []Change // edits applied through the commands
	Planned    []Edit   // a dry run's edits
	Rejected   []Rejection
	Unrecorded []Change // pages the agent changed without a command, recorded afterwards
}

// Dream runs one dream session over the store under its dream.lock. spin is
// the path of the binary the agent runs its commands with.
//
// Ensures: returns errLocked, doing nothing, if another dream holds dream.lock.
// Ensures: pages changed by hand outside spin are recorded (by cli) before
// the session, and pages the agent changed without a command after it (by
// dream), so every page change stays in the history.
// Ensures: the checkpoint moves only after a session that finished, and
// never in a dry run. A session that failed keeps what it applied; the next
// one sees those pages and goes on from them.
func (p Project) Dream(ctx context.Context, agent model.Agent, spin string, mode DreamMode, now time.Time) (DreamResult, error) {
	var res DreamResult
	if err := p.Init(); err != nil {
		return res, err
	}
	unlock, err := lockFile(filepath.Join(p.Dir, "dream.lock"), false)
	if err != nil {
		return res, err
	}
	defer unlock()
	began := time.Now()
	res, err = p.dream(ctx, agent, spin, mode, now)
	var files []string
	for _, c := range append(res.Changes, res.Unrecorded...) {
		files = append(files, c.Target.String())
	}
	note := fmt.Sprintf("%d entries, %d changed, %d rejected", res.Fresh, len(res.Changes), len(res.Rejected))
	if mode == DreamDryRun {
		note = fmt.Sprintf("dry run: %d entries, %d planned, %d rejected", res.Fresh, len(res.Planned), len(res.Rejected))
	}
	if len(res.Unrecorded) > 0 {
		note += fmt.Sprintf(", %d changed without a command", len(res.Unrecorded))
	}
	p.Log(Event{At: now, Kind: "dream", Project: p.Name, Files: files, Ms: time.Since(began).Milliseconds(), Note: note + ". " + res.Summary, Err: errString(err)})
	return res, err
}

func (p Project) dream(ctx context.Context, agent model.Agent, spin string, mode DreamMode, now time.Time) (DreamResult, error) {
	var res DreamResult
	if _, err := p.reconcilePages(ByCLI, "edited by hand outside spin", now); err != nil {
		return res, err
	}
	in, err := p.LoadDreamInput(now)
	if err != nil {
		return res, err
	}
	res.Fresh = len(in.Fresh)
	if len(in.Fresh) == 0 {
		res.Summary = "no new entries"
		return res, nil
	}
	sess := dreamSession{Home: p.Home, Store: p.Name, Mode: mode, Started: now.UTC()}
	for _, e := range in.Fresh {
		sess.Fresh = append(sess.Fresh, e.ID)
	}
	os.Remove(p.dreamOutcomesPath())
	if err := writeJSON(p.dreamSessionPath(), sess); err != nil {
		return res, err
	}
	defer os.Remove(p.dreamSessionPath())
	defer os.Remove(p.dreamOutcomesPath())

	summary, runErr := agent.Run(ctx, model.Session{
		Dir:       p.Dir,
		System:    dreamSystem,
		Prompt:    dreamTask(in, spin, p.dreamSessionPath(), mode),
		Commands:  dreamCommands(spin),
		ReadDirs:  append([]string{p.Home}, append(sourceDirs(p.Home), in.Folders...)...),
		WriteDirs: []string{p.Home},
	})
	res.Summary = oneLine(summary)
	outcomes, err := readJSONL[dreamOutcome](p.dreamOutcomesPath())
	if err != nil {
		return res, err
	}
	changes, _ := p.Changes()
	byID := map[string]Change{}
	for _, c := range changes {
		byID[c.ID] = c
	}
	for _, o := range outcomes {
		switch {
		case o.Rejected != "":
			res.Rejected = append(res.Rejected, Rejection{Edit: o.Edit, Why: o.Rejected})
		case o.Planned:
			res.Planned = append(res.Planned, o.Edit)
		case o.Change != "":
			res.Changes = append(res.Changes, byID[o.Change])
		}
	}
	if res.Unrecorded, err = p.reconcilePages(ByDream, "changed during a dream without spin dream write", now); err != nil {
		return res, err
	}
	if runErr != nil {
		return res, fmt.Errorf("dream %s: %w", p.Name, runErr)
	}
	if mode == DreamDryRun {
		return res, nil
	}
	return res, writeJSON(p.checkpointPath(), in.Through)
}

// dreamCommands are the commands a dream session may run: the two that
// write, and the read-only ones.
func dreamCommands(spin string) []string {
	var out []string
	for _, c := range []string{"dream write", "dream retire", "zoom", "search", "log", "rules list", "rules show", "wiki list", "wiki show"} {
		out = append(out, spin+" "+c)
	}
	return out
}

// sourceDirs are the folders the configured sources read, so a dream can
// open the transcripts entries point at.
func sourceDirs(home string) []string {
	srcs, _ := source.Load(home)
	var out []string
	for _, s := range srcs {
		if d, ok := s.(interface{ Dir() string }); ok && d.Dir() != "" {
			out = append(out, d.Dir())
		}
	}
	return out
}

// reconcilePages records, as by, every page whose file differs from what
// page-changes.jsonl last recorded for it: a page edited, created or
// deleted without ApplyEdits.
func (p Project) reconcilePages(by Actor, reason string, now time.Time) ([]Change, error) {
	unlock, err := p.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	changes, err := p.Changes()
	if err != nil {
		return nil, err
	}
	recorded := map[PageRef]string{}
	for _, c := range changes {
		recorded[c.Target] = c.After
	}
	onDisk := map[PageRef]string{}
	for _, layer := range []Layer{RulesLayer, WikiLayer} {
		paths, _ := filepath.Glob(filepath.Join(p.Dir, string(layer), "*.md"))
		for _, path := range paths {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			onDisk[PageRef{Layer: layer, Slug: Slug(strings.TrimSuffix(filepath.Base(path), ".md"))}] = string(b)
		}
	}
	var out []Change
	record := func(ref PageRef, before, after string) {
		out = append(out, Change{ID: "c" + strings.TrimPrefix(string(NewEntryID(now)), "e"), At: now.UTC(), By: by, Target: ref,
			Before: before, After: after, Reason: reason})
	}
	for ref, text := range onDisk {
		if recorded[ref] != text {
			record(ref, recorded[ref], text)
		}
	}
	for ref, text := range recorded {
		if _, ok := onDisk[ref]; !ok && text != "" {
			record(ref, text, "")
		}
	}
	slices.SortFunc(out, func(a, b Change) int { return strings.Compare(a.Target.String(), b.Target.String()) })
	return out, appendJSONL(p.changesPath(), out)
}

// ApplyDreamEdit is spin dream write and spin dream retire: one edit, checked
// against the running dream's session and applied, or in a dry run recorded.
// It returns the text the agent sees.
//
// Ensures: an error, writing nothing, unless the session's dream is running.
// Ensures: a rejected edit is an error saying why, so the agent can fix it.
func ApplyDreamEdit(sessionPath string, req DreamEdit, now time.Time) (string, error) {
	var sess dreamSession
	if err := readJSON(sessionPath, &sess); err != nil || sess.Store == "" {
		return "", fmt.Errorf("%s isn't a dream session: these commands work only inside a dream spindle started", sessionPath)
	}
	p := Store(sess.Home, sess.Store)
	if unlock, err := lockFile(filepath.Join(p.Dir, "dream.lock"), false); err == nil {
		unlock()
		return "", fmt.Errorf("no dream of %s is running", p.Name)
	}
	in := DreamInput{Store: p.Name}
	es, err := p.Entries()
	if err != nil {
		return "", err
	}
	fresh := map[EntryID]bool{}
	for _, id := range sess.Fresh {
		fresh[id] = true
	}
	for _, e := range es {
		if fresh[e.ID] {
			in.Fresh = append(in.Fresh, e)
		}
	}
	if in.Rules, err = p.Rules(); err != nil {
		return "", err
	}
	if in.Wiki, err = p.Wiki(); err != nil {
		return "", err
	}
	outcome := func(o dreamOutcome) { _ = appendJSONL(p.dreamOutcomesPath(), []dreamOutcome{o}) }

	e, err := req.edit(in, now)
	if err == nil {
		if _, rejected := CheckEdits(in, []Edit{e}); len(rejected) > 0 {
			err = errors.New(rejected[0].Why)
		}
	}
	if err != nil {
		outcome(dreamOutcome{Edit: e, Rejected: err.Error()})
		return "", fmt.Errorf("rejected %s: %w; fix it and run the command again", req.Target, err)
	}
	if sess.Mode == DreamDryRun {
		outcome(dreamOutcome{Edit: e, Planned: true})
		return fmt.Sprintf("Would save %s (dry run: nothing is written).", e.Target), nil
	}
	changes, err := p.ApplyEdits([]Edit{e}, ByDream, now)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return e.Target.String() + " is already like that.", nil
	}
	outcome(dreamOutcome{Edit: e, Change: changes[0].ID})
	if e.After == "" {
		return "Retired " + e.Target.String() + ".", nil
	}
	return "Saved " + e.Target.String() + ".", nil
}

// FormatDream renders what a dream did, for spin dream.
func FormatDream(r DreamResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d new entries. %s\n", r.Fresh, orDefault(r.Summary, "(no summary)"))
	for _, c := range r.Changes {
		what := "wrote"
		if c.After == "" {
			what = "retired"
		}
		fmt.Fprintf(&b, "  %s %s: %s\n", what, c.Target, c.Reason)
	}
	for _, e := range r.Planned {
		what := "would write"
		if e.After == "" {
			what = "would retire"
		}
		fmt.Fprintf(&b, "  %s %s: %s\n", what, e.Target, e.Reason)
	}
	for _, x := range r.Rejected {
		fmt.Fprintf(&b, "  rejected %s: %s\n", x.Edit.Target, x.Why)
	}
	for _, c := range r.Unrecorded {
		fmt.Fprintf(&b, "  changed without a command, now recorded: %s\n", c.Target)
	}
	return b.String()
}
