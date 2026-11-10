package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"spindle/internal/model"
)

func TestDreamDue(t *testing.T) {
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

// A whole dream session: the commands apply checked edits and refuse bad
// ones, a page the agent wrote around the commands is recorded afterwards, a
// hand edit before the session is recorded as the user's, the checkpoint
// moves only after a finished session, and a dry run writes nothing.
func TestDream(t *testing.T) {
	p := project(t)
	if err := p.Append([]Entry{
		entry(t, "e-1", "s1", t0, "Byron wants PR titles to start with feat: or fix:."),
		entry(t, "e-2", "s2", t0.Add(time.Hour), "Deploys moved from Jenkins to GitHub Actions on Nov 3."),
	}); err != nil {
		t.Fatal(err)
	}
	hand := WikiPage{Page: Page{Slug: "jenkins", Title: "Jenkins", Body: "Old CI.\n"}, Kind: KindTool}
	os.MkdirAll(filepath.Join(p.Dir, "wiki"), 0o755)
	os.WriteFile(filepath.Join(p.Dir, "wiki", "jenkins.md"), []byte(hand.Render()), 0o644) // by hand, not through spin

	write := func(target string, title string, kind EntityKind, body string, evidence ...EntryID) (string, error) {
		return ApplyDreamEdit(p.dreamSessionPath(), DreamEdit{Target: target, Title: title, Kind: kind, Body: body, Evidence: evidence, Reason: "from the notes"}, t0)
	}
	agent := model.AgentFunc(func(ctx context.Context, s model.Session) (model.Reply, error) {
		if !strings.Contains(s.Prompt, "e-2") || !strings.Contains(s.Prompt, "dream write --session") {
			t.Errorf("task lacks the notes or the commands:\n%s", s.Prompt)
		}
		if _, err := write("rules/git-and-prs", "Git and PRs", "", "- PR titles start with feat: or fix:.", "e-1"); err != nil {
			t.Errorf("write: %v", err)
		}
		if _, err := write("wiki/ghost", "Ghost", KindTool, "x", "e-9"); err == nil {
			t.Error("an edit citing a note it wasn't shown was accepted")
		}
		if _, err := write("wiki/deploy-pipeline", "Deploy pipeline", KindTool, "GitHub Actions since 2026-11-03.", "e-2"); err != nil {
			t.Errorf("write: %v", err)
		}
		os.WriteFile(filepath.Join(p.Dir, "wiki", "stray.md"), []byte(WikiPage{Page: Page{Slug: "stray", Title: "Stray"}, Kind: KindConcept}.Render()), 0o644)
		return model.Reply{Text: "one rule, one page", By: "fake"}, nil
	})

	dry, err := p.Dream(context.Background(), agent, "spin", DreamDryRun, t0)
	if err != nil || len(dry.Planned) != 2 || len(dry.Changes) != 0 {
		t.Fatalf("dry run: %+v, %v", dry, err)
	}
	if text, _ := p.PageText(PageRef{RulesLayer, "git-and-prs"}); text != "" {
		t.Fatal("the dry run wrote a page")
	}
	if cp, _ := p.LoadCheckpoint(); cp.Read != 0 {
		t.Fatalf("the dry run moved the checkpoint: %+v", cp)
	}
	os.Remove(filepath.Join(p.Dir, "wiki", "stray.md"))
	p.reconcilePages(ByCLI, "test cleanup", t0)

	res, err := p.Dream(context.Background(), agent, "spin", DreamApply, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 2 || len(res.Rejected) != 1 || res.Rejected[0].Edit.Target.Slug != "ghost" {
		t.Fatalf("changes %d, rejected %+v; want 2 applied and the ghost rejected", len(res.Changes), res.Rejected)
	}
	if res.By != "fake" {
		t.Errorf("result by %q, want the agent that finished", res.By)
	}
	if len(res.Unrecorded) != 1 || res.Unrecorded[0].Target.Slug != "stray" || res.Unrecorded[0].By != ByDream {
		t.Errorf("unrecorded %+v, want the stray page, by dream", res.Unrecorded)
	}
	byTarget := map[Slug]Actor{}
	changes, _ := p.Changes()
	for _, c := range changes {
		if _, ok := byTarget[c.Target.Slug]; !ok {
			byTarget[c.Target.Slug] = c.By
		}
	}
	if byTarget["jenkins"] != ByCLI {
		t.Errorf("the hand edit is recorded by %q, want cli", byTarget["jenkins"])
	}
	wiki, _ := p.Wiki()
	for _, w := range wiki {
		if w.Slug == "deploy-pipeline" && !slices.Equal(w.Evidence, []EntryID{"e-2"}) {
			t.Errorf("evidence %v", w.Evidence)
		}
	}
	if cp, _ := p.LoadCheckpoint(); cp.Read != 2 || cp.Last != "e-2" {
		t.Errorf("checkpoint %+v", cp)
	}
	if _, err := os.Stat(p.dreamSessionPath()); err == nil {
		t.Error("the session file outlived the dream")
	}
	if _, err := write("rules/late", "Late", "", "x", "e-1"); err == nil {
		t.Error("a dream command worked with no dream running")
	}

	// A failed session keeps what it applied and leaves the checkpoint.
	p.Append([]Entry{entry(t, "e-3", "s3", t0.Add(3*time.Hour), "The staging cluster is shared with QA.")})
	failing := model.AgentFunc(func(ctx context.Context, s model.Session) (model.Reply, error) {
		write("wiki/staging", "Staging", KindTool, "Shared with QA.", "e-3")
		return model.Reply{}, errors.New("the model gave up")
	})
	if _, err := p.Dream(context.Background(), failing, "spin", DreamApply, t0.Add(4*time.Hour)); err == nil {
		t.Fatal("a failed session: want an error")
	}
	if text, _ := p.PageText(PageRef{WikiLayer, "staging"}); text == "" {
		t.Error("the failed session's edit was lost")
	}
	if cp, _ := p.LoadCheckpoint(); cp.Read != 2 {
		t.Errorf("a failed session moved the checkpoint to %d", cp.Read)
	}
}
