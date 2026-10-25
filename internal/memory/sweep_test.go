package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindle/internal/source"
	"spindle/internal/transcript"
)

func TestSweep(t *testing.T) {
	p := project(t)
	dir := t.TempDir()
	src, _ := source.New(source.Spec{Kind: source.ClaudeCode, Dir: dir})
	srcs := []source.Source{src}
	now := t0.Add(24 * time.Hour)
	at := func(path string, d time.Duration) { os.Chtimes(path, now.Add(d), now.Add(d)) }

	before := filepath.Join(dir, "before.jsonl")
	claudeTranscript(t, before, p.Cwd, t0, "An old session from before spindle swept anything.", "ok")
	at(before, -2*time.Hour)
	if err := Sweep(context.Background(), p.Home, srcs, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(pending(p.Home)) != 0 {
		t.Fatal("the first sweep queued history")
	}

	fresh := filepath.Join(dir, "fresh.jsonl")
	claudeTranscript(t, fresh, p.Cwd, t0, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	at(fresh, -30*time.Minute)
	read := filepath.Join(dir, "read.jsonl")
	claudeTranscript(t, read, p.Cwd, t0, "A session a hook already queued and extraction read.", "ok")
	at(read, -40*time.Minute)
	updateCursors(p.Home, func(c map[string]*Cursor) { c[read] = &Cursor{Line: 2, At: now.Add(-35 * time.Minute)} })
	live := filepath.Join(dir, "live.jsonl")
	claudeTranscript(t, live, p.Cwd, t0, "A session that is still going, its hooks will queue it.", "ok")
	at(live, -time.Minute)

	if err := Sweep(context.Background(), p.Home, srcs, now); err != nil {
		t.Fatal(err)
	}
	jobs := pending(p.Home)
	if len(jobs) != 1 {
		t.Fatalf("%d jobs, want the fresh session only", len(jobs))
	}
	var j Job
	readJSON(jobs[0], &j)
	if j.Transcript != fresh || j.Cwd != p.Cwd || j.Source != source.ClaudeCode || j.Agent != "claude" || j.Session != "sess-fresh.jsonl" {
		t.Errorf("job %+v", j)
	}
	marks, _ := Marks(p.Home)
	if m := marks[source.ClaudeCode]; m.Found != 2 || m.Queued != 1 || !m.Swept.Equal(now.Add(-SweepSettle)) {
		t.Errorf("mark %+v", m)
	}
	c := NewClient(p.Home, p.Cwd, "", "cli")
	out, err := c.Sources()
	if err != nil || !strings.Contains(out, "2 sessions changed, 1 queued") {
		t.Errorf("sources: %v\n%s", err, out)
	}
}

type brokenSource struct{}

func (brokenSource) Kind() string { return "broken" }
func (brokenSource) List(context.Context, time.Time, time.Time) ([]source.Ref, error) {
	return nil, os.ErrPermission
}
func (brokenSource) Read(context.Context, string) (transcript.Session, error) {
	return transcript.Session{}, nil
}

func TestSweepKeepsAFailedSourcesMark(t *testing.T) {
	p := project(t)
	writeJSON(marksPath(p.Home), map[string]*Mark{"broken": {Swept: t0}})
	if err := Sweep(context.Background(), p.Home, []source.Source{brokenSource{}}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	marks, _ := Marks(p.Home)
	if m := marks["broken"]; !m.Swept.Equal(t0) || m.Err == "" {
		t.Errorf("mark %+v", m)
	}
}

// A source added again starts from when it was added, not from its old mark.
func TestSweepReAddedSourceStartsAfresh(t *testing.T) {
	p := project(t)
	dir := t.TempDir()
	now := t0.Add(24 * time.Hour)
	writeJSON(marksPath(p.Home), map[string]*Mark{source.Codex: {Swept: now.Add(-48 * time.Hour)}})
	old := filepath.Join(dir, "while-removed.jsonl")
	claudeTranscript(t, old, p.Cwd, t0, "A session from while the source was removed.", "ok")
	os.Chtimes(old, now.Add(-time.Hour), now.Add(-time.Hour))
	src, _ := source.New(source.Spec{Kind: source.Codex, Dir: dir, Added: now.Add(-30 * time.Minute)})
	if err := Sweep(context.Background(), p.Home, []source.Source{src}, now); err != nil {
		t.Fatal(err)
	}
	if len(pending(p.Home)) != 0 {
		t.Fatal("a re-added source read sessions from before it was added")
	}
	if m, _ := Marks(p.Home); !m[source.Codex].Swept.Equal(now.Add(-30 * time.Minute)) {
		t.Errorf("mark %+v", m[source.Codex])
	}
}

// A source added a moment ago waits for sessions to settle, without
// restarting itself on each sweep.
func TestSweepJustAddedSource(t *testing.T) {
	p := project(t)
	now := t0.Add(24 * time.Hour)
	added := now.Add(-time.Minute)
	src, _ := source.New(source.Spec{Kind: source.Codex, Dir: t.TempDir(), Added: added})
	for i := 0; i < 2; i++ {
		if err := Sweep(context.Background(), p.Home, []source.Source{src}, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if m, _ := Marks(p.Home); !m[source.Codex].Swept.Equal(added) {
		t.Errorf("mark %+v", m[source.Codex])
	}
	evs, _ := p.Events()
	n := 0
	for _, e := range evs {
		if e.Kind == "sweep" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d sweep events, want one first-sweep note", n)
	}
}
