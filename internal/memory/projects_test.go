package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectsShareEverything(t *testing.T) {
	a := project(t) // "proj", linked to its folder
	b := Store(a.Home, "eldspire")
	b.UpdateMeta(func(m *Meta) { m.Description = "D&D campaign notes" })
	c := a.Client("cli")
	// A user memory goes global by default; a directive stays in the
	// session's project unless another project is named.
	if msg, _ := c.Write(Op{Name: "role", Type: "user", Description: "Byron, solo dev", Body: "Go."}); msg != "Saved global/role.md." {
		t.Errorf("user write: %q", msg)
	}
	c.Write(Op{Name: "no-attribution", Type: "directive", Description: "No Claude attribution in commits or PRs", Body: "Never.", Project: "global"})
	if msg, _ := c.Write(Op{Name: "go-vet", Type: "directive", Description: "Run go vet before committing", Body: "Run it."}); msg != "Saved proj/go-vet.md." {
		t.Errorf("directive write: %q", msg)
	}
	if msg, _ := c.Write(Op{Name: "eldspire/session-day", Type: "project", Description: "Eldspire sessions run on Sundays", Body: "Sundays."}); msg != "Saved eldspire/session-day.md." {
		t.Errorf("write to another project: %q", msg)
	}
	// Updating a global memory by bare name updates it in place.
	c.Write(Op{Name: "role", Type: "user", Description: "Byron, solo dev on spindle", Body: "Go and TS."})
	if _, ok := a.Get("role"); ok {
		t.Error("role leaked into the project store")
	}

	// A session in eldspire starts with its own index and global, and a line
	// for proj; it can search and recall proj's memories.
	sb := b.StartContext()
	for _, want := range []string{"# eldspire (this folder's project)", "D&D campaign", "[session-day](eldspire/session-day.md)", "[no-attribution](global/no-attribution.md)", "solo dev on spindle", "- proj (1 memories)"} {
		if !strings.Contains(sb, want) {
			t.Errorf("eldspire start context lacks %q:\n%s", want, sb)
		}
	}
	if strings.Contains(sb, "go-vet") {
		t.Errorf("another project's index in full:\n%s", sb)
	}
	hits, total, _ := b.Client("cli").Search("go vet", 0)
	if len(hits) != 1 || hits[0].Name != "proj/go-vet" || total != 4 {
		t.Errorf("search from eldspire: %+v of %d", hits, total)
	}
	id, _ := b.StartRecall("s", "claude", "should I run go vet before committing?", "", time.Now())
	RunSelect(context.Background(), b.Home, "s", id, nil, true)
	if text, _ := b.WaitRecall("s", id, 0); !strings.Contains(text, "Memory: proj/go-vet.md") {
		t.Errorf("cross-project recall: %q", text)
	}

	// Bare names resolve to the session's project, then global, then anywhere.
	if m, err := b.Client("cli").Read("go-vet"); err != nil || m.Name != "proj/go-vet" {
		t.Errorf("bare read across projects: %+v %v", m, err)
	}
	if h, _ := c.History("global/role"); len(h) != 2 {
		t.Errorf("history %d", len(h))
	}
	if msg, err := c.Delete("role", "test"); err != nil || msg != "Deleted global/role.md." {
		t.Errorf("delete: %q %v", msg, err)
	}
	if s, _ := b.Client("cli").Status(); s.Global != 1 || s.Total != 1 || s.Projects != 1 || s.Elsewhere != 1 {
		t.Errorf("status %+v", s)
	}
}

func TestNoProjectForFolder(t *testing.T) {
	t.Setenv(ProjectEnv, "")
	home, dir := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	c := NewClient(home, filepath.Join(dir, "sub"), "", "claude")
	if !strings.Contains(c.Index(), "isn't linked to a project") {
		t.Errorf("index:\n%s", c.Index())
	}
	// A project fact needs a project; a user fact goes global.
	if _, err := c.Write(Op{Name: "deadline", Type: "project", Description: "Ship by Friday", Body: "Friday."}); err == nil {
		t.Error("project write with no project: want an error")
	}
	if msg, _ := c.Write(Op{Name: "role", Type: "user", Description: "Solo dev", Body: "Go."}); msg != "Saved global/role.md." {
		t.Errorf("user write: %q", msg)
	}
	// Naming a new project creates it, but a write never links the folder:
	// it could be about any project.
	msg, err := c.Write(Op{Name: "deadline", Type: "project", Description: "Ship by Friday", Body: "Friday.", Project: "Taxes 2026", ProjectDescription: "Filing 2026 taxes"})
	if err != nil || msg != "Saved taxes-2026/deadline.md. New project taxes-2026." {
		t.Fatalf("new project: %q %v", msg, err)
	}
	if p := c.Context(); p.Name != "" || Store(home, "taxes-2026").Meta().Description != "Filing 2026 taxes" {
		t.Errorf("folder hints %+v", p)
	}
	// Linking takes the repository root.
	if msg, err := c.SetProject("taxes-2026", "", ".", ""); err != nil || msg != "Linked "+dir+"." {
		t.Errorf("link: %q %v", msg, err)
	}
	if p := c.Context(); p.Name != "taxes-2026" {
		t.Errorf("folder now hints %+v", p)
	}
	// Linking a folder to an existing project from elsewhere.
	other := t.TempDir()
	if msg, err := NewClient(home, other, "", "cli").SetProject("taxes-2026", "", ".", ""); err != nil || !strings.Contains(msg, "Linked "+other) {
		t.Errorf("link: %q %v", msg, err)
	}
	ps, _ := c.Projects()
	if len(ps) != 2 || ps[0].Name != GlobalName || ps[1].Name != "taxes-2026" || len(ps[1].Folders) != 2 || !ps[1].Context {
		t.Errorf("projects %+v", ps)
	}
}

func TestMigrateRepoStores(t *testing.T) {
	t.Setenv(ProjectEnv, "")
	home, root := t.TempDir(), t.TempDir()
	old := filepath.Join(home, "projects", "spindle-569f1393")
	os.MkdirAll(filepath.Join(old, "memory"), 0o755)
	writeJSON(filepath.Join(old, "project.json"), map[string]any{"root": root, "created": t0})
	os.WriteFile(filepath.Join(old, "memory", "x.md"), []byte(Memory{Name: "x", Type: "directive", Description: "d", Body: "b"}.Render()), 0o644)
	writeJSON(filepath.Join(old, "state.json"), map[string]any{"cursors": map[string]any{"/t.jsonl": map[string]any{"agent": "claude", "line": 7}}, "sessions_since_dream": map[string]string{"s": "/t.jsonl"}})
	appendJSONL(filepath.Join(old, "events.jsonl"), []Event{{At: t0, Kind: "start", Session: "s"}})

	p, err := Hint(home, root)
	if err != nil || p.Name != "spindle" {
		t.Fatalf("hint after migration: %+v %v", p, err)
	}
	if m, ok := p.Get("x"); !ok || m.Body != "b" {
		t.Error("memory lost")
	}
	if cur, _ := Cursors(home); cur["/t.jsonl"] == nil || cur["/t.jsonl"].Line != 7 {
		t.Errorf("cursors %+v", cur)
	}
	if st, _ := p.LoadState(); st.Sessions["s"] != "/t.jsonl" {
		t.Errorf("dream gate %+v", st)
	}
	if evs, _ := p.Events(); len(evs) != 1 || evs[0].Project != "spindle" {
		t.Errorf("events %+v", evs)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old folder still there")
	}
}
