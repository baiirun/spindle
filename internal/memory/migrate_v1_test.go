package memory

import (
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeV1(t *testing.T, s Project, name, typ, desc, body string) string {
	t.Helper()
	path := filepath.Join(s.Dir, "memory", name+".md")
	text := "---\nname: " + name + "\ndescription: " + desc + "\nmetadata:\n  type: " + typ + "\n---\n\n" + body + "\n"
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	appendJSONL(filepath.Join(s.Dir, "changes.jsonl"), []v1Change{{At: t0, Op: "write", Name: name, By: "agent", New: text}})
	return path
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(path)
			out[path] = string(b)
		}
		return nil
	})
	return out
}

// Each v1 type lands in its layer, v1's files are untouched, and the
// migration converges: a rerun adds nothing, a rerun after a lost page
// restores just that page.
func TestMigrateV1(t *testing.T) {
	p := project(t)
	g := Store(p.Home, GlobalName)
	g.Init()
	writeV1(t, g, "no-attribution", "directive", "No Claude attribution in commits", "Never.")
	writeV1(t, g, "role", "user", "Byron, solo dev", "Go and TS.")
	writeV1(t, p, "deploy-pipeline", "project", "Deploys run on GitHub Actions", "Since November.")
	writeV1(t, p, "grafana", "reference", "Latency dashboard in Grafana", "grafana.internal/d/api")
	writeV1(t, p, "odd", "feedback", "A type v1 never had", "Still a memory.")
	v1 := map[string]map[string]string{}
	for _, s := range []Project{g, p} {
		v1[s.Name] = snapshot(t, filepath.Join(s.Dir, "memory"))
	}

	if _, err := MigrateV1(p.Home, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rs, _ := g.Rules(); len(rs) != 2 {
		t.Errorf("global rules %d, want directive and user", len(rs))
	}
	kinds := map[Slug]EntityKind{}
	pages, _ := p.Wiki()
	for _, w := range pages {
		kinds[w.Slug] = w.Kind
		if len(w.Evidence) != 1 {
			t.Errorf("%s cites %v, want its memory's entry", w.Slug, w.Evidence)
		}
	}
	if kinds["deploy-pipeline"] != KindProject || kinds["grafana"] != KindLink || kinds["odd"] != KindConcept {
		t.Errorf("wiki kinds %v", kinds)
	}
	for _, s := range []Project{g, p} {
		if got := snapshot(t, filepath.Join(s.Dir, "memory")); !maps.Equal(got, v1[s.Name]) {
			t.Errorf("%s: v1 files changed", s.Name)
		}
	}

	before, _ := p.Entries()
	os.Remove(filepath.Join(p.Dir, "wiki", "grafana.md")) // as if it crashed before this page
	if _, err := MigrateV1(p.Home, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, _ := p.Entries()
	if len(after) != len(before) {
		t.Errorf("rerun appended %d entries", len(after)-len(before))
	}
	if text, _ := p.PageText(PageRef{Layer: WikiLayer, Slug: "grafana"}); text == "" {
		t.Error("rerun didn't restore the missing page")
	}
}
