package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/episode"
)

// TestBuildNoLeak: nothing at or after the cutoff reaches the snapshot, from
// transcripts or episodes, and earlier content survives intact.
func TestBuildNoLeak(t *testing.T) {
	root := t.TempDir()
	corpusRoot, episodeRoot := filepath.Join(root, "corpus"), filepath.Join(root, "episodes")
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	mk := func(index int, texts ...string) corpus.Chunk {
		c := corpus.Chunk{Source: "codex", Session: "sess", Index: index}
		for i, text := range texts {
			line := index*10 + i
			c.Items = append(c.Items, corpus.Item{ID: corpus.ItemID("codex", "sess", line), Line: line, Time: t0.Add(time.Duration(line) * time.Minute), Role: corpus.RoleUser, Text: text})
		}
		c.Start, c.End = c.Items[0].Time, c.Items[len(c.Items)-1].Time
		return c
	}
	chunks := []corpus.Chunk{mk(1, "early one", "early two"), mk(2, "before cut", "AFTER CUT"), mk(3, "LATER CHUNK")}
	for _, c := range chunks {
		if _, err := c.Write(corpusRoot); err != nil {
			t.Fatal(err)
		}
		e := episode.Episode{ID: "ep", Source: "codex", Session: "sess", Chunk: c.Index, Start: c.Start, End: c.End, Scope: "x", Status: episode.StatusSummary, Title: "EPISODE " + c.Items[len(c.Items)-1].Text}
		if err := episode.Write(episodeRoot, e); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := chunks[1].Items[1].Time // the "AFTER CUT" item
	out := filepath.Join(root, "snap")
	st, err := Build(Options{CorpusRoot: corpusRoot, EpisodeRoot: episodeRoot, Out: out, Version: "v", Before: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if st.Chunks != 2 || st.Truncated != 1 || st.Episodes != 1 {
		t.Fatalf("stats = %+v", st)
	}
	var all strings.Builder
	filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	got := all.String()
	for _, want := range []string{"early one", "early two", "before cut", "EPISODE early two"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, leak := range []string{"AFTER CUT", "LATER CHUNK"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q", leak)
		}
	}
	if _, err := Build(Options{CorpusRoot: corpusRoot, Out: out, Version: "v", Before: cutoff}); err == nil {
		t.Fatal("Build overwrote an existing snapshot")
	}
}
