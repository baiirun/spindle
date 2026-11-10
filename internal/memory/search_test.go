package memory

import (
	"strings"
	"testing"
	"time"
)

// Search answers with evidence, and zoom finds an id in whichever store has
// it: both stores for a shared day, one with a store prefix.
func TestSearchAndZoom(t *testing.T) {
	p := project(t)
	g := Store(p.Home, GlobalName)
	day := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	p.Append([]Entry{entry(t, "e-p", "s1", day, "Deploys moved from Jenkins to GitHub Actions.")})
	g.Append([]Entry{entry(t, "e-g", "s2", day, "Byron prefers table-driven Go tests.")})
	page := WikiPage{Page: Page{Slug: "deploy-pipeline", Title: "Deploy pipeline", Body: "GitHub Actions.\n", Evidence: []EntryID{"e-p"}}, Kind: KindTool}
	p.ApplyEdits([]Edit{{Target: PageRef{WikiLayer, "deploy-pipeline"}, After: page.Render(), Reason: "r"}}, ByCLI, day)

	c, err := LoadCorpus(p.Home)
	if err != nil {
		t.Fatal(err)
	}
	hits := Search(c, "deploy pipeline", []SearchLayer{SearchWiki}, 5)
	if len(hits) != 1 || hits[0].ID != "wiki/deploy-pipeline" || len(hits[0].Evidence) != 1 || hits[0].Evidence[0].ID != "e-p" {
		t.Fatalf("hits %+v", hits)
	}

	both, err := Zoom(c, "d:2026-11-08")
	if err != nil || !strings.Contains(both, "## proj") || !strings.Contains(both, "## global") {
		t.Errorf("zoom on a shared day:\n%s %v", both, err)
	}
	one, _ := Zoom(c, "proj/d:2026-11-08")
	if strings.Contains(one, "table-driven") || !strings.Contains(one, "s:s1") {
		t.Errorf("zoom with a store prefix:\n%s", one)
	}
	if _, err := Zoom(c, "d:1999-01-01"); err == nil {
		t.Error("zoom on an id no store has: want an error")
	}
}
