package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"spindle/internal/transcript"
)

func session(t *testing.T, p Project, msgs ...string) transcript.Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, path, p.Cwd, t0, msgs...)
	s, err := transcript.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func gistsReply(line int) string {
	return fmt.Sprintf(`{"gists":[
{"gist":"Deploys go through the Buildkite pipeline ship-it.","line":%d,"scope":"project"},
{"gist":"Byron wants table-driven Go tests.","line":%d,"scope":"global"}]}`, line, line)
}

// A pass that appended and then died before moving its cursor must not
// append the same gists again.
func TestIngestRerunAfterCrash(t *testing.T) {
	p := project(t)
	s := session(t, p, "Our deploys go through Buildkite, the pipeline is ship-it. And write Go tests table-driven.", "Noted.")
	m := &replies{out: []string{gistsReply(s.Messages[0].Line)}}
	res, err := Ingest(context.Background(), p, s, m, t0)
	if err != nil || res.Project != 1 || res.Global != 1 {
		t.Fatalf("first pass: %+v, %v", res, err)
	}
	// The crash: the cursor never moved.
	updateCursors(p.Home, func(c map[string]*Cursor) { delete(c, s.Path) })

	again := &replies{} // any model call fails
	res, err = Ingest(context.Background(), p, s, again, t0)
	if err != nil || res.Project+res.Global != 0 || len(again.prompts) != 0 {
		t.Fatalf("rerun: %+v, %v, %d model calls; want nothing new and no call", res, err, len(again.prompts))
	}
	for _, st := range []Project{p, Store(p.Home, GlobalName)} {
		if es, _ := st.Entries(); len(es) != 1 {
			t.Errorf("%s has %d entries, want 1", st.Name, len(es))
		}
	}
}

// A failed model call appends nothing and keeps the cursor, so the next
// pass reads the same messages.
func TestIngestModelFailureKeepsCursor(t *testing.T) {
	p := project(t)
	s := session(t, p, "We decided the log is the only source of truth for memory.", "Agreed.")
	if _, err := Ingest(context.Background(), p, s, &replies{out: []string{"not json"}}, t0); err == nil {
		t.Fatal("bad reply: want an error")
	}
	if cur, _ := Cursors(p.Home); cur[s.Path] != nil && cur[s.Path].Line != 0 {
		t.Errorf("cursor moved to %d", cur[s.Path].Line)
	}
	if es, _ := p.Entries(); len(es) != 0 {
		t.Errorf("%d entries appended", len(es))
	}
}

// Without a project, project-scoped gists wait in unfiled with their folder,
// and linking the folder later brings them into the project exactly once.
func TestUnfiledUntilLinked(t *testing.T) {
	p := project(t)
	folder := t.TempDir()
	none := Project{Home: p.Home, Cwd: folder}
	s := session(t, none, "Our deploys go through Buildkite, the pipeline is ship-it. And write Go tests table-driven.", "Noted.")
	res, err := Ingest(context.Background(), none, s, &replies{out: []string{gistsReply(s.Messages[0].Line)}}, t0)
	if err != nil || res.Global != 1 || res.Unfiled != 1 || res.Project != 0 {
		t.Fatalf("ingest: %+v, %v", res, err)
	}
	unfiled := Store(p.Home, UnfiledName)
	elsewhere := entry(t, "e-other", "x", t0, "From another unlinked folder.")
	elsewhere.Folder = t.TempDir()
	unfiled.Append([]Entry{elsewhere})

	c := NewClient(p.Home, folder, "", "cli")
	for range 2 { // linking twice adopts once
		if _, err := c.SetProject("proj", "", folder, ""); err != nil {
			t.Fatal(err)
		}
	}
	es, _ := p.Entries()
	if len(es) != 1 || es[0].Adopted == "" || es[0].Folder != folder || es[0].Gist != "Deploys go through the Buildkite pipeline ship-it." {
		t.Fatalf("project entries %+v, want the one unfiled gist from this folder", es)
	}
	if u, _ := unfiled.Entries(); len(u) != 2 {
		t.Errorf("unfiled has %d entries, want both still there", len(u))
	}
	corp, _ := LoadCorpus(p.Home)
	if hits := Search(corp, "buildkite", nil, 10); len(hits) != 1 || hits[0].Store != "proj" {
		t.Errorf("search found %+v, want only the adopted copy", hits)
	}
}
