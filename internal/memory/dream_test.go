package memory

import (
	"fmt"
	"testing"
	"time"
)

func TestDreamDue(t *testing.T) {
	t.Skip("pending: DreamDue")
	var es []Entry
	for i := range 6 {
		es = append(es, entry(t, fmt.Sprintf("e-%d", i), fmt.Sprintf("s%d", i%5), t0, "x")) // sessions s0..s4
	}
	day := t0.Add(DreamEvery)
	for _, c := range []struct {
		name string
		cp   Checkpoint
		now  time.Time
		want bool
	}{
		{"never dreamed, 5 sessions", Checkpoint{}, t0, true},
		{"5 sessions, but dreamed an hour ago", Checkpoint{At: day.Add(-time.Hour)}, day, false},
		{"a day later, 5 sessions", Checkpoint{At: t0}, day, true},
		{"a day later, only 4 sessions unread", Checkpoint{Read: 2, Last: "e-1", At: t0}, day, false},
	} {
		if got := DreamDue(c.cp, es, c.now); got != c.want {
			t.Errorf("%s: due %v, want %v", c.name, got, c.want)
		}
	}
}

// Dream's edits are checked against what it was given; a bad edit is
// dropped with a reason and doesn't sink the rest.
func TestCheckEdits(t *testing.T) {
	t.Skip("pending: CheckEdits, Rule.Render")
	fresh := entry(t, "e-new", "s", t0, "Byron wants PR titles as feat: or fix:.")
	old := Rule{Page{Slug: "git-and-prs", Title: "Git and PRs", Body: "- No Claude attribution.\n", Evidence: []EntryID{"e-old"}}}
	in := DreamInput{Fresh: []Entry{fresh}, Rules: []Rule{old}}
	updated := old
	updated.Body += "- PR titles start with feat: or fix:.\n"
	updated.Evidence = []EntryID{"e-old", "e-new"}
	newRule := func(slug string) string { return Rule{Page{Slug: Slug(slug), Title: "T", Body: "- x\n"}}.Render() }
	ref := func(slug string) PageRef { return PageRef{Layer: RulesLayer, Slug: Slug(slug)} }

	for _, c := range []struct {
		name string
		edit Edit
		ok   bool
	}{
		{"update citing fresh and existing evidence", Edit{Target: ref("git-and-prs"), After: updated.Render(), Evidence: []EntryID{"e-new"}, Reason: "new PR title rule"}, true},
		{"no reason", Edit{Target: ref("git-and-prs"), After: updated.Render(), Evidence: []EntryID{"e-new"}}, false},
		{"cites an entry it wasn't shown", Edit{Target: ref("git-and-prs"), After: updated.Render(), Evidence: []EntryID{"e-elsewhere"}, Reason: "r"}, false},
		{"text that isn't a page", Edit{Target: ref("git-and-prs"), After: "no frontmatter", Evidence: []EntryID{"e-new"}, Reason: "r"}, false},
		{"retires a page that doesn't exist", Edit{Target: ref("nope"), Evidence: []EntryID{"e-new"}, Reason: "r"}, false},
		{"creates a new topic", Edit{Target: ref("testing"), After: newRule("testing"), Evidence: []EntryID{"e-new"}, Reason: "r"}, true},
	} {
		ok, rejected := CheckEdits(in, []Edit{c.edit})
		if (len(ok) == 1) != c.ok || len(ok)+len(rejected) != 1 {
			t.Errorf("%s: kept %d, rejected %+v; want kept %v", c.name, len(ok), rejected, c.ok)
		}
	}
}
