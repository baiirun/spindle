package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// history makes n entries, one every hour, across sessions of 10.
func history(t *testing.T, n int, start time.Time) []Entry {
	t.Helper()
	var es []Entry
	for i := range n {
		es = append(es, entry(t, fmt.Sprintf("e-%05d", i), fmt.Sprintf("s%04d", i/10), start.Add(time.Duration(i)*time.Hour), fmt.Sprintf("gist number %d", i)))
	}
	return es
}

func size(lines []TimelineLine) int {
	n := 0
	for _, l := range lines {
		n += len(l.ID) + len(l.Text) + 2 // "<id> <text>\n"
	}
	return n
}

// The OptMem shape: within budget, the newest entries word for word, the
// oldest history as week lines, nothing shown twice.
func TestFitTimelineDecaysWithAge(t *testing.T) {
	es := history(t, 2000, t0) // about 12 weeks
	tl := Timeline(es, rollAll(t, es, t0), time.UTC)
	const budget = 6000
	lines := FitTimeline(tl, budget)
	if size(lines) > budget {
		t.Fatalf("%d bytes, budget %d", size(lines), budget)
	}
	if last := lines[len(lines)-1]; last.ID != "e-01999" {
		t.Errorf("newest line is %q, want the newest entry verbatim", last.ID)
	}
	if !strings.HasPrefix(lines[0].ID, "w:") {
		t.Errorf("oldest line is %q, want a week", lines[0].ID)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l.ID] {
			t.Errorf("%s shown twice", l.ID)
		}
		seen[l.ID] = true
	}
}

// With no rollups at all (a new install, a side model that is down), the
// index still works: it shows entries, newest kept, and says what it left out.
func TestFitTimelineWithoutRollups(t *testing.T) {
	tl := Timeline(history(t, 500, t0), nil, time.UTC)
	const budget = 3000
	lines := FitTimeline(tl, budget)
	if size(lines) > budget {
		t.Fatalf("%d bytes, budget %d", size(lines), budget)
	}
	if lines[0].ID != "" || !strings.Contains(lines[0].Text, "memory_zoom") {
		t.Errorf("first line %+v, want a note on the older entries left out", lines[0])
	}
	if last := lines[len(lines)-1]; last.ID != "e-00499" {
		t.Errorf("newest line is %q, want e-00499", last.ID)
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l.ID, "e-") {
			t.Errorf("%q has no rollup, so it can't be shown as one line", l.ID)
		}
	}
}

// The index is capped for any input, and every rule topic is named in it.
func TestRenderIndexCap(t *testing.T) {
	var rules []Rule
	for i := range 12 { // about 12 KB: far past RulesBytes
		rules = append(rules, Rule{Page{Slug: Slug(fmt.Sprintf("topic-%02d", i)), Title: "Topic", Body: strings.Repeat("- do the thing carefully\n", 40)}})
	}
	tl := Timeline(history(t, 3000, t0), nil, time.UTC)
	view := StoreView{Name: "proj", Rules: rules, Timeline: tl}
	var others []ProjectInfo
	for i := range 40 {
		others = append(others, ProjectInfo{Meta: Meta{Name: fmt.Sprintf("other-%d", i), Description: strings.Repeat("x", 100)}})
	}
	out := RenderIndex(IndexInput{Global: StoreView{Name: GlobalName, Rules: rules, Timeline: tl}, Project: &view, Others: others, Now: t0.Add(4000 * time.Hour)})
	if len(out) > IndexBytes {
		t.Fatalf("%d bytes, cap %d", len(out), IndexBytes)
	}
	for _, r := range rules {
		if !strings.Contains(out, string(r.Slug)) {
			t.Errorf("rule topic %s missing", r.Slug)
		}
	}
}
