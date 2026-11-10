package memory

import (
	"reflect"
	"testing"
)

// Rendering and parsing are inverse, so a page read, edited and written
// back is the same page.
func TestPageRoundTrip(t *testing.T) {
	w := WikiPage{Page: Page{Slug: "deploy-pipeline", Title: "Deploy pipeline", Body: "Runs on GitHub Actions.\n\n---\nnot frontmatter: just text\n",
		Evidence: []EntryID{"e-1", "e-2"}, Updated: t0}, Kind: KindTool}
	got, err := ParseWikiPage("deploy-pipeline", w.Render())
	if err != nil || !reflect.DeepEqual(got, w) {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	if _, err := ParseWikiPage("other-slug", w.Render()); err == nil {
		t.Error("a page parsed under another slug")
	}
}

// Applying the same edits twice records them once, so a dream rerun after a
// crash converges.
func TestApplyEditsTwiceRecordsOnce(t *testing.T) {
	p := project(t)
	page := WikiPage{Page: Page{Slug: "deploy-pipeline", Title: "Deploy pipeline", Body: "GitHub Actions.\n", Evidence: []EntryID{"e-1"}, Updated: t0}, Kind: KindTool}
	edits := []Edit{{Target: PageRef{Layer: WikiLayer, Slug: "deploy-pipeline"}, After: page.Render(), Evidence: []EntryID{"e-1"}, Reason: "moved off Jenkins"}}
	for range 2 {
		if _, err := p.ApplyEdits(edits, ByDream, t0); err != nil {
			t.Fatal(err)
		}
	}
	if ch, _ := p.Changes(); len(ch) != 1 || ch[0].Before != "" || ch[0].Reason != "moved off Jenkins" {
		t.Fatalf("changes %+v", ch)
	}
	retire := []Edit{{Target: edits[0].Target, Reason: "the pipeline was removed"}}
	if _, err := p.ApplyEdits(retire, ByCLI, t0); err != nil {
		t.Fatal(err)
	}
	if text, _ := p.PageText(edits[0].Target); text != "" {
		t.Error("retired page still there")
	}
	if ch, _ := p.Changes(); len(ch) != 2 || ch[1].After != "" || ch[1].Before != page.Render() {
		t.Fatalf("retirement not recorded with the text it removed: %+v", ch)
	}
}
