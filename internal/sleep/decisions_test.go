package sleep

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

func TestRunStopsSchedulingAfterUsageLimit(t *testing.T) {
	corpusRoot := t.TempDir()
	for i := 1; i <= 3; i++ {
		at := time.Date(2026, 9, 23, 12, i, 0, 0, time.UTC)
		chunk := corpus.Chunk{
			Source: "codex", Session: "session", Cwd: "/work/Zaum", Index: i, Start: at, End: at,
			Items: []corpus.Item{{ID: corpus.ItemID("codex", "session", i), Line: i, Time: at, Role: corpus.RoleUser, Text: "a decision"}},
		}
		if _, err := chunk.Write(corpusRoot); err != nil {
			t.Fatal(err)
		}
	}

	calls := 0
	extract := func(context.Context, string, string, string, string, any) (llm.Result, error) {
		calls++
		return llm.Result{}, llm.ErrUsageLimit
	}

	_, _, _, err := Run(context.Background(), Options{
		CorpusRoot: corpusRoot,
		OutRoot:    filepath.Join(t.TempDir(), "memory"),
		Slice:      "Zaum",
		Workers:    1,
		Model:      llm.Extractor,
		extract:    extract,
	})
	if !errors.Is(err, llm.ErrUsageLimit) {
		t.Fatalf("Run error = %v, want usage limit", err)
	}
	if calls != 1 {
		t.Fatalf("extractor calls = %d, want 1 after usage limit", calls)
	}
}
