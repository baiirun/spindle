package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindle/internal/model"
)

// Edge cases of the pieces covered in memory_test.go: what must never be
// lost, what must stay inside the context budget, and how recall and
// extraction behave when a model answers badly or not at all.

// --- never lose a memory ---

func TestRestoreEveryKindOfChange(t *testing.T) {
	p := project(t)
	c := p.Client("cli")
	c.Write(Op{Name: "db", Type: "project", Description: "Database choice", Body: "SQLite."})
	c.Write(Op{Name: "db", Body: "Postgres."})
	if _, err := p.Apply([]Op{{Op: "write", Name: "db", Body: "Postgres 17.", Reason: "merged"}}, "dream", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	c.Delete("db", "wrong")
	body := func() string {
		m, err := c.Read("db")
		if err != nil {
			return "(missing)"
		}
		return strings.TrimSpace(m.Body)
	}
	// History is newest first: delete, dream, overwrite, create.
	if _, err := c.Restore("db", 1); err != nil || body() != "Postgres 17." {
		t.Fatalf("undo delete: %v, body %q", err, body())
	}
	// Now: restore, delete, dream, overwrite, create. Undo the dream's change.
	if _, err := c.Restore("db", 3); err != nil || body() != "Postgres." {
		t.Fatalf("undo dream: %v, body %q", err, body())
	}
	if m, _ := c.Read("db"); m.Type != "project" || m.Description != "Database choice" {
		t.Errorf("restore lost the frontmatter: %+v", m)
	}
	hist, _ := c.History("db")
	if len(hist) != 6 || hist[0].By != "cli" || !strings.Contains(hist[0].Reason, "restored") {
		t.Errorf("history after restores: %+v", hist)
	}
}

// A memory deleted from global or another project can be restored by the
// bare name the session saw it under.
func TestRestoreDeletedMemoryInAnotherStore(t *testing.T) {
	p := project(t)
	c := p.Client("cli")
	c.Write(Op{Name: "role", Type: "user", Description: "Solo dev", Body: "Go."})
	if msg, err := c.Delete("role", "test"); err != nil || msg != "Deleted global/role.md." {
		t.Fatalf("delete: %q %v", msg, err)
	}
	if hist, _ := c.History("role"); len(hist) != 2 {
		t.Errorf("history of a deleted global memory by bare name: %d changes, want 2", len(hist))
	}
	if _, err := c.Restore("role", 1); err != nil {
		t.Errorf("restore by bare name: %v", err)
	}
	if hist, _ := c.History("global/role"); len(hist) != 2 {
		t.Fatalf("history by full name: %d changes, want 2", len(hist))
	}
}

func TestNamesThatNormalizeAlikeShareAFile(t *testing.T) {
	p := project(t)
	write(t, p, Op{Op: "write", Name: "Selector Model", Type: "project", Description: "Selector runs Sonnet", Body: "Sonnet."})
	write(t, p, Op{Op: "write", Name: "selector_model.md", Body: "Haiku."})
	mems, _ := p.Load()
	if len(mems) != 1 || mems[0].Name != "selector-model" || mems[0].Body != "Haiku." {
		t.Fatalf("memories %+v", mems)
	}
	if ch, _ := p.Changes(); len(ch) != 2 || ch[1].Old == "" {
		t.Errorf("the overwrite was not logged with its old text: %+v", ch)
	}
}

// Writing an existing name updates it where it is (the agent is told this in
// Instructions), even when it lives in a project other than the session's.
func TestBareWriteUpdatesMemoryInOtherProject(t *testing.T) {
	a := project(t)
	a.Client("cli").Write(Op{Name: "go-vet", Type: "directive", Description: "Run go vet before committing", Body: "Run it."})
	b := Store(a.Home, "eldspire")
	b.Init()
	c := b.Client("codex")
	if m, err := c.Read("go-vet"); err != nil || m.Name != "proj/go-vet" {
		t.Fatalf("bare read: %+v %v", m, err)
	}
	msg, err := c.Write(Op{Name: "go-vet", Body: "Run go vet ./... and fix what it reports."})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if msg != "Saved proj/go-vet.md." {
		t.Errorf("write went to %q, want proj/go-vet", msg)
	}
	if _, ok := b.Get("go-vet"); ok {
		t.Error("a second go-vet was created in eldspire")
	}
}

// Claude Code's own memories use type "feedback"; a hand-copied or
// hand-edited file with a type spindle doesn't know must not vanish from the
// index while recall and search still see it.
func TestUnknownTypeStaysInIndex(t *testing.T) {
	p := project(t)
	write(t, p, Op{Op: "write", Name: "role", Type: "user", Description: "Solo dev", Body: "Go."})
	text := "---\nname: old-rule\ndescription: Copied from Claude Code\nmetadata:\n  type: feedback\n---\n\nKeep replies short.\n"
	if err := os.WriteFile(filepath.Join(p.MemDir(), "old-rule.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.RebuildIndex(); err != nil {
		t.Fatal(err)
	}
	mems, _ := p.LoadAll()
	if len(mems) != 2 {
		t.Fatalf("loaded %d memories, want 2", len(mems))
	}
	if idx := p.ReadIndex(); !strings.Contains(idx, "old-rule") && !strings.Contains(idx, "not listed") {
		t.Errorf("index silently drops a memory recall can still pick:\n%s", idx)
	}
}

func TestParseKeepsColonsAndRulesInText(t *testing.T) {
	m := Memory{Name: "ports", Type: "reference", Description: "Ports: API 8080, admin 9090", Body: "Before\n\n---\n\nafter the rule."}
	got, err := Parse(m.Render())
	if err != nil || got.Description != m.Description || got.Body != m.Body {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if _, err := Parse("no frontmatter"); err == nil {
		t.Error("want error without frontmatter")
	}
	if _, err := Parse("---\nname: x\n"); err == nil {
		t.Error("want error for unterminated frontmatter")
	}
}

// --- dreams ---

func dreamReady(t *testing.T, p Project) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, path, p.Cwd, t0, "Keep replies short from now on.", "OK.")
	st, _ := p.LoadState()
	for _, s := range []string{"a", "b", "c", "d", "sess-s.jsonl"} {
		st.Sessions[s] = path
	}
	st.LastDream = t0.Add(-48 * time.Hour)
	p.SaveState(st)
}

// A dream sees one store. An op naming another store's memory must not land
// in this store under the bare name.
func TestDreamStaysInItsStore(t *testing.T) {
	p := project(t)
	other := Store(p.Home, "other")
	other.Apply([]Op{{Op: "write", Name: "foo", Type: "project", Description: "Other's foo", Body: "Other."}}, "agent", "", t0)
	write(t, p, Op{Op: "write", Name: "bar", Type: "project", Description: "Bar", Body: "Bar."})
	dreamReady(t, p)
	m := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		return `{"ops":[{"op":"write","name":"other/foo","type":"project","description":"Rewritten","body":"Rewritten.","reason":""}],"summary":"s"}`, nil
	})
	if _, err := Dream(context.Background(), p, m, t0); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Get("foo"); ok {
		t.Error("dream wrote other/foo into this store as proj/foo")
	}
	if got, _ := other.Get("foo"); got.Body != "Other." {
		t.Errorf("other/foo changed: %+v", got)
	}
}

// Sessions that touch a store while its dream waits on the model belong to
// the next dream; resetting the gate must not drop them.
func TestDreamKeepsSessionsThatArriveDuringIt(t *testing.T) {
	p := project(t)
	write(t, p, Op{Op: "write", Name: "bar", Type: "project", Description: "Bar", Body: "Bar."})
	dreamReady(t, p)
	m := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		if err := p.markSession("late", "/t/late.jsonl"); err != nil {
			t.Error(err)
		}
		return `{"ops":[],"summary":"nothing"}`, nil
	})
	if _, err := Dream(context.Background(), p, m, t0); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.LoadState(); st.Sessions["late"] == "" {
		t.Errorf("a session marked during the dream was dropped: %+v", st.Sessions)
	}
}

func TestDreamDueStoresRunsOnlyOpenGates(t *testing.T) {
	p := project(t)
	write(t, p, Op{Op: "write", Name: "bar", Type: "project", Description: "Bar", Body: "Bar."})
	quiet := Store(p.Home, "quiet")
	quiet.Apply([]Op{{Op: "write", Name: "x", Type: "project", Description: "X", Body: "X."}}, "agent", "", t0)
	dreamReady(t, p)
	calls := 0
	m := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		calls++
		return `{"ops":[],"summary":"ok"}`, nil
	})
	if n := DreamDueStores(context.Background(), p.Home, m, t0, io.Discard); n != 1 || calls != 1 {
		t.Fatalf("dreamed %d stores with %d calls, want 1", n, calls)
	}
	// The gate is now closed: a second run does nothing.
	if n := DreamDueStores(context.Background(), p.Home, m, t0.Add(time.Hour), io.Discard); n != 0 || calls != 1 {
		t.Fatalf("second run dreamed %d stores", n)
	}
}

// Invariant 18: one writer per store at a time. Agents, the CLI and a dream
// writing at once lose no update, and the index lists every file.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	p := project(t)
	dreamReady(t, p)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			caller := []string{"claude", "codex", "cli"}[i%3]
			if _, err := p.Client(caller).Write(Op{Name: fmt.Sprintf("fact-%02d", i), Type: "project", Description: "fact", Body: "b"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		m := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
			return `{"ops":[{"op":"write","name":"dreamed","type":"project","description":"from a dream","body":"d","reason":""}],"summary":"s"}`, nil
		})
		if _, err := Dream(context.Background(), p, m, t0); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	mems, _ := p.Load()
	ch, _ := p.Changes()
	if len(mems) != 21 || len(ch) != 21 {
		t.Fatalf("%d memories, %d changes logged, want 21 each", len(mems), len(ch))
	}
	if n := strings.Count(p.ReadIndex(), "\n- ["); n != 21 {
		t.Errorf("index lists %d of 21 memories:\n%s", n, p.ReadIndex())
	}
}

// --- fixed budget ---

func TestStartContextBudgetAtScale(t *testing.T) {
	p := project(t)
	desc := strings.Repeat("a fairly specific one-line description ", 4)
	for i := 0; i < 30; i++ {
		s := Store(p.Home, fmt.Sprintf("project-%02d", i))
		s.UpdateMeta(func(m *Meta) { m.Description = desc })
		var ops []Op
		for j := 0; j < 40; j++ {
			ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("m-%02d", j), Type: "project", Description: desc, Body: "b"})
		}
		s.Apply(ops, "agent", "", t0)
	}
	var ops []Op
	for j := 0; j < 300; j++ {
		ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("p-%03d", j), Type: "project", Description: desc + "ünïcödé ✓", Body: "b"})
	}
	write(t, p, ops...)
	s := p.StartContext()
	if n := len([]rune(s)); n > StartBudget {
		t.Fatalf("start context is %d characters, budget %d", n, StartBudget)
	}
}

// A busy project's index must not push global rules ("never add Claude
// attribution") out of the session start.
func TestStartContextKeepsGlobalWhenProjectIsBig(t *testing.T) {
	p := project(t)
	g := Store(p.Home, GlobalName)
	g.Apply([]Op{{Op: "write", Name: "no-attribution", Type: "directive", Description: "Never add Claude attribution to commits or PRs", Body: "Never."}}, "agent", "", t0)
	Store(p.Home, "eldspire").UpdateMeta(func(m *Meta) { m.Description = "D&D campaign" })
	var ops []Op
	for j := 0; j < 80; j++ {
		ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("decision-%02d", j), Type: "project", Description: "A project decision with a description long enough to be useful in an index line", Body: "b"})
	}
	write(t, p, ops...)
	s := p.StartContext()
	for _, want := range []string{"global/no-attribution.md", "- eldspire"} {
		if !strings.Contains(s, want) {
			t.Errorf("start context (%d chars) lost %q to the project's index", len(s), want)
		}
	}
}

// With 200 project memories the project section is cut on its own: global
// and the other projects still load, the project's directives and newest
// memories are kept, and a line counts what was left out.
func TestStartContextCutsWithinProjectSection(t *testing.T) {
	p := project(t)
	g := Store(p.Home, GlobalName)
	g.Apply([]Op{{Op: "write", Name: "no-attribution", Type: "directive", Description: "Never add Claude attribution to commits or PRs", Body: "Never."}}, "agent", "", t0)
	Store(p.Home, "eldspire").UpdateMeta(func(m *Meta) { m.Description = "D&D campaign" })
	var ops []Op
	for j := 0; j < 200; j++ {
		ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("decision-%03d", j), Type: "project", Description: "A project decision with a description long enough to be useful in an index line", Body: "b"})
	}
	write(t, p, ops...)
	write(t, p, Op{Op: "write", Name: "old-rule", Type: "directive", Description: "Run the linter before pushing", Body: "b"})
	// Make the rule the oldest memory and decision-199 the newest.
	for j := 0; j < 200; j++ {
		at := t0.Add(time.Duration(j+1) * time.Minute)
		os.Chtimes(filepath.Join(p.MemDir(), fmt.Sprintf("decision-%03d.md", j)), at, at)
	}
	os.Chtimes(filepath.Join(p.MemDir(), "old-rule.md"), t0, t0)

	s := p.StartContext()
	if n := len([]rune(s)); n > StartBudget {
		t.Fatalf("start context is %d characters, budget %d", n, StartBudget)
	}
	for _, want := range []string{"global/no-attribution.md", "- eldspire", "proj/old-rule.md", "proj/decision-199.md", "more in proj, use memory_search"} {
		if !strings.Contains(s, want) {
			t.Errorf("start context lost %q:\n%s", want, s[len(Instructions):])
		}
	}
	if strings.Contains(s, "proj/decision-000.md") {
		t.Errorf("the oldest decision was kept over newer ones")
	}
	if g, pr := strings.Index(s, "# global"), strings.Index(s, "# proj"); g < 0 || pr < 0 || g > pr {
		t.Errorf("global should come before the project (at %d and %d)", g, pr)
	}
}

// memory_index isn't bound by the start budget: it lists every memory.
func TestIndexToolIsNotCut(t *testing.T) {
	p := project(t)
	var ops []Op
	for j := 0; j < 120; j++ {
		ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("m-%03d", j), Type: "project", Description: "A project decision with a description long enough to be useful", Body: "b"})
	}
	write(t, p, ops...)
	if n := strings.Count(p.Client("cli").Index(), "\n- ["); n != 120 {
		t.Errorf("index lists %d of 120 memories", n)
	}
}

// The global dream is told global's room at session start and how much it
// uses, so it can keep global small enough to load whole.
func TestGlobalDreamIsToldItsRoom(t *testing.T) {
	p := project(t)
	g := Store(p.Home, GlobalName)
	var ops []Op
	for j := 0; j < 30; j++ {
		ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("rule-%02d", j), Type: "directive", Description: "A rule that applies in every project, described at some length", Body: "b"})
	}
	g.Apply(ops, "agent", "", t0)
	var calls []string
	m := fake(t, &calls, func(system, prompt string) string {
		if !strings.Contains(prompt, fmt.Sprintf("in about %d characters", GlobalRoom())) || !strings.Contains(prompt, "That is over") {
			t.Errorf("global dream prompt lacks its size:\n%s", prompt[strings.Index(prompt, "## Instruction files"):])
		}
		return `{"ops":[],"summary":"nothing to change"}`
	})
	if _, err := Dream(context.Background(), g, m, t0); err != nil {
		t.Fatal(err)
	}
	m = fake(t, &calls, func(system, prompt string) string {
		if strings.Contains(prompt, "## Size") {
			t.Errorf("a project's dream was given global's size")
		}
		return `{"ops":[],"summary":"nothing to change"}`
	})
	if _, err := Dream(context.Background(), p, m, t0); err != nil {
		t.Fatal(err)
	}
}

// Invariant 13: each recalled file is cut at 4 KB, whatever its script.
func TestRecalledFileCutAtFourKilobytes(t *testing.T) {
	for _, body := range []string{strings.Repeat("a", 6000), strings.Repeat("é", 3000), strings.Repeat("✓", 2000)} {
		m := Memory{Name: "x", Type: "project", Description: "d", Body: body, Modified: t0}
		text := recalled(m, t0)
		if len(text) > FileBytes+200 {
			t.Errorf("recalled %d bytes for a %d-byte body, cap %d", len(text), len(body), FileBytes)
		}
	}
}

func TestRecallPicksAtMostFiveKnownFiles(t *testing.T) {
	p := project(t)
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "dup"} {
		write(t, p, Op{Op: "write", Name: n, Type: "project", Description: "memory " + n, Body: n})
	}
	Store(p.Home, "other").Apply([]Op{{Op: "write", Name: "dup", Type: "project", Description: "other dup", Body: "x"}}, "agent", "", t0)
	id, _ := p.StartRecall("s", "", "which memories apply here?", "", t0)
	m := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		return `{"selected_memories":["a.md","a.md","proj/b.md","b","made-up.md","dup.md","c","d.md","e.md","f.md"]}`, nil
	})
	if err := RunSelect(context.Background(), p.Home, "s", id, m, false); err != nil {
		t.Fatal(err)
	}
	var files []string
	p.withRecall("s", func(st *RecallState) error { files = st.Pending.Files; return nil })
	if strings.Join(files, ",") != "proj/a.md,proj/b.md,proj/c.md,proj/d.md,proj/e.md" {
		t.Errorf("picks %v", files)
	}
}

func TestRecallStopsAtSessionBudget(t *testing.T) {
	p := project(t)
	var ops []Op
	for i := 0; i < 40; i++ {
		ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("big-%02d", i), Type: "project", Description: "big", Body: strings.Repeat("x", 3900)})
	}
	write(t, p, ops...)
	next := 0
	m := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		var picks []string
		for i := 0; i < 5 && next < 40; i++ {
			picks = append(picks, fmt.Sprintf("%q", fmt.Sprintf("big-%02d.md", next)))
			next++
		}
		return `{"selected_memories":[` + strings.Join(picks, ",") + `]}`, nil
	})
	total, stopped := 0, false
	for turn := 0; turn < 8; turn++ {
		id, _ := p.StartRecall("s", "", fmt.Sprintf("question number %d", turn), "", t0)
		if id == "" {
			stopped = true
			break
		}
		RunSelect(context.Background(), p.Home, "s", id, m, false)
		text, _ := p.AttachPending("s")
		total += len(text)
	}
	// Like Claude Code, the check runs before a message, so the last message
	// may go over by at most one message's worth.
	if !stopped || total > SessionBytes+SelectMax*(FileBytes+300) {
		t.Errorf("stopped %v after %d bytes", stopped, total)
	}
}

func TestNewerPromptSupersedesSelection(t *testing.T) {
	p := project(t)
	write(t, p, Op{Op: "write", Name: "event-log-store", Type: "project", Description: "Event log lives in Postgres", Body: "Postgres."})
	old, _ := p.StartRecall("s", "", "where is the event log stored?", "", t0)
	cur, _ := p.StartRecall("s", "", "never mind, where is the event log kept?", "", t0.Add(time.Second))
	RunSelect(context.Background(), p.Home, "s", old, nil, true)
	if text, _ := p.AttachPending("s"); text != "" {
		t.Fatalf("a superseded selection attached: %q", text)
	}
	RunSelect(context.Background(), p.Home, "s", cur, nil, true)
	if text, _ := p.AttachPending("s"); !strings.Contains(text, "event-log-store") {
		t.Fatalf("current selection = %q", text)
	}
}

func TestSelectorFailureAttachesNothing(t *testing.T) {
	p := project(t)
	write(t, p, Op{Op: "write", Name: "a", Type: "project", Description: "A", Body: "a"})
	id, _ := p.StartRecall("s", "claude", "what is in memory a?", "", t0)
	m := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		return "", errors.New("claude: not signed in")
	})
	RunSelect(context.Background(), p.Home, "s", id, m, false)
	if text, _ := p.AttachPending("s"); text != "" {
		t.Fatalf("attached after a failed selection: %q", text)
	}
	evs, _ := p.Events()
	if len(evs) == 0 || evs[len(evs)-1].Kind != "select" || !strings.Contains(evs[len(evs)-1].Err, "not signed in") {
		t.Errorf("failure not in the activity log: %+v", evs)
	}
}

// The selector sees the 200 newest files across every project, so a busy
// project pushes a quiet project's memories out of view. This pins the
// current behavior; changing it is a design decision.
func TestSelectorSeesNewest200AcrossProjects(t *testing.T) {
	p := project(t)
	quiet := Store(p.Home, "quiet")
	quiet.Apply([]Op{{Op: "write", Name: "old-fact", Type: "project", Description: "Quiet project fact", Body: "x"}}, "agent", "", t0)
	os.Chtimes(filepath.Join(quiet.MemDir(), "old-fact.md"), t0, t0)
	var ops []Op
	for i := 0; i < SelectCandidates; i++ {
		ops = append(ops, Op{Op: "write", Name: fmt.Sprintf("busy-%03d", i), Type: "project", Description: "busy", Body: "b"})
	}
	write(t, p, ops...)
	id, _ := p.StartRecall("s", "", "what was the quiet project fact?", "", t0)
	var prompt string
	m := model.Func(func(ctx context.Context, system, pr, schema string) (string, error) {
		prompt = pr
		return `{"selected_memories":[]}`, nil
	})
	RunSelect(context.Background(), p.Home, "s", id, m, false)
	if strings.Contains(prompt, "quiet/old-fact.md") {
		t.Error("expected the 201st newest file to be out of the selector's view")
	}
}

// --- extraction ---

func TestExtractFailureKeepsCursor(t *testing.T) {
	p := project(t)
	path := filepath.Join(t.TempDir(), "a.jsonl")
	claudeTranscript(t, path, p.Cwd, t0, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	broken := model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		return "", errors.New("codex: exit 1")
	})
	if _, err := Extract(context.Background(), p, path, broken, t0); err == nil {
		t.Fatal("want the model's error")
	}
	if cur, _ := Cursors(p.Home); cur[path] != nil {
		t.Fatalf("cursor moved past messages that were never read: %+v", cur[path])
	}
	if st, _ := p.LoadState(); len(st.Sessions) != 0 {
		t.Errorf("failed pass counted for the dream gate: %+v", st)
	}
	var calls []string
	ok := fake(t, &calls, func(system, prompt string) string {
		if strings.Contains(system, "Step 1 of 2") {
			return `{"nothing_to_save":false,"read":[]}`
		}
		return `{"ops":[{"op":"write","name":"deploy-pipeline","type":"reference","description":"Deploys run in Buildkite ship-it","body":"ship-it","reason":""}]}`
	})
	if res, err := Extract(context.Background(), p, path, ok, t0); err != nil || res.Written != 1 {
		t.Fatalf("retry = %+v, %v", res, err)
	}
	if cur, _ := Cursors(p.Home); cur[path] == nil || cur[path].Line != 2 {
		t.Errorf("cursor after retry: %+v", cur[path])
	}
}

// After a long gap or repeated failures, messages past the window are cut
// from the front. That must show in the activity log, not only in the prompt.
func TestExtractLongGapCutIsLogged(t *testing.T) {
	p := project(t)
	path := filepath.Join(t.TempDir(), "a.jsonl")
	var msgs []string
	for i := 0; i < 30; i++ {
		msgs = append(msgs, fmt.Sprintf("message %d %s", i, strings.Repeat("word ", 1000)))
	}
	claudeTranscript(t, path, p.Cwd, t0, msgs...)
	var calls []string
	m := fake(t, &calls, func(string, string) string { return `{"nothing_to_save":true,"read":[]}` })
	if _, err := Extract(context.Background(), p, path, m, t0); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "earlier messages cut") || strings.Contains(calls[0], "message 0 ") {
		t.Fatalf("prompt was not cut to the window")
	}
	evs, _ := p.Events()
	var note string
	for _, e := range evs {
		if e.Kind == "extract" {
			note = e.Note
		}
	}
	if !strings.Contains(note, "cut") {
		t.Errorf("extract event %q doesn't say messages were cut", note)
	}
}

func TestExtractSkipsSubagentTranscripts(t *testing.T) {
	p := project(t)
	path := filepath.Join(t.TempDir(), "sub.jsonl")
	line := `{"type":"user","sessionId":"x","cwd":"` + p.Cwd + `","isSidechain":true,"timestamp":"2026-10-25T04:00:00Z","message":{"content":"Review this diff for bugs please, carefully."}}`
	os.WriteFile(path, []byte(line+"\n"), 0o644)
	var calls []string
	m := fake(t, &calls, func(string, string) string { return `{"nothing_to_save":true,"read":[]}` })
	if res, err := Extract(context.Background(), p, path, m, t0); err != nil || res.Skip != "subagent" || len(calls) != 0 {
		t.Fatalf("subagent = %+v, %v, %d calls", res, err, len(calls))
	}
}

// Extraction ops are routed like the agent's writes, and the dream gate
// counts the session for every store it changed.
func TestExtractRoutesAndCountsEveryStoreTouched(t *testing.T) {
	p := project(t)
	path := filepath.Join(t.TempDir(), "a.jsonl")
	claudeTranscript(t, path, p.Cwd, t0, "I'm Byron. Never add Claude attribution anywhere. Eldspire sessions moved to Saturdays.", "Noted.")
	var calls []string
	m := fake(t, &calls, func(system, prompt string) string {
		if strings.Contains(system, "Step 1 of 2") {
			return `{"nothing_to_save":false,"read":[]}`
		}
		return `{"ops":[
{"op":"write","name":"role","type":"user","description":"Byron","body":"Byron.","project":""},
{"op":"write","name":"no-attribution","type":"directive","description":"No Claude attribution","body":"Never.","project":"global"},
{"op":"write","name":"session-day","type":"project","description":"Sessions on Saturdays","body":"Saturdays.","project":"eldspire","project_description":"D&D campaign"},
{"op":"write","name":"broken","type":"feedback","description":"bad type","body":"x","project":""}]}`
	})
	res, err := Extract(context.Background(), p, path, m, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := "+global/role,+global/no-attribution,+eldspire/session-day"
	if strings.Join(res.Changed, ",") != want || res.Result.Skipped != 1 {
		t.Errorf("changed %v (skipped %d), want %s", res.Changed, res.Result.Skipped, want)
	}
	if d := Store(p.Home, "eldspire").Meta().Description; d != "D&D campaign" {
		t.Errorf("new project description %q", d)
	}
	if len(Store(p.Home, "eldspire").Meta().Folders) != 0 {
		t.Error("extraction linked a folder")
	}
	for _, s := range []string{"proj", "global", "eldspire"} {
		if st, _ := Store(p.Home, s).LoadState(); st.Sessions["sess-a.jsonl"] == "" {
			t.Errorf("%s: session not counted for the dream gate", s)
		}
	}
}
