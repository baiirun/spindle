package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/sleep"
)

// TestMaterializeNoLeak checks the core eval invariant: nothing at or after
// the cutoff reaches the reader, whether from transcripts or notes.
func TestMaterializeNoLeak(t *testing.T) {
	root := t.TempDir()
	corpusRoot := filepath.Join(root, "corpus")
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s := corpus.Session{Source: "codex", ID: "aaaaaaaa-1111", Cwd: "/x/Zaum"}
	for i, text := range []string{"before one", "before two", "QUESTION", "after one"} {
		s.Items = append(s.Items, corpus.Item{
			ID: corpus.ItemID("codex", s.ID, i+1), Line: i + 1,
			Time: t0.Add(time.Duration(i) * time.Minute), Role: corpus.RoleUser, Text: text,
		})
	}
	for _, c := range corpus.SplitChunks(s) {
		if _, err := c.Write(corpusRoot); err != nil {
			t.Fatal(err)
		}
	}
	notesRoot := filepath.Join(root, "notes-design")
	for _, n := range []string{"early.md", "late.md"} {
		p := filepath.Join(notesRoot, "notes", "zaum", n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(n), 0o644)
	}
	cutoff := t0.Add(2 * time.Minute) // the QUESTION item
	notes := []sleep.Note{
		{ID: "d-early", Path: "zaum/early.md", AvailableAt: t0.Add(time.Minute)},
		{ID: "d-late", Path: "zaum/late.md", AvailableAt: cutoff},
	}
	idx, err := loadChunkIndex(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "mat")
	if err := Materialize(dir, Arm{Name: "D2", Transcripts: true, Notes: notesRoot}, idx, notes, cutoff); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	got := all.String()
	for _, want := range []string{"before one", "before two", "early.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, leak := range []string{"QUESTION", "after one", "late.md"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q", leak)
		}
	}
}
