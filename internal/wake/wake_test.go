package wake

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/episode"
)

func TestSearchStartsWithEpisodeAndReadFollowsSafeRef(t *testing.T) {
	root := t.TempDir()
	episodeRoot := filepath.Join(root, "episodes")
	corpusRoot := filepath.Join(root, "corpus")
	e := episode.Episode{ID: "ep-codex-session-0001", Source: "codex", Session: "session", Chunk: 1, Scope: "spindle", Title: "Spindle continuity", Observations: []episode.Claim{{Text: "Build wake search for durable context.", Evidence: []string{"codex:session#L1"}}}}
	if err := episode.Write(episodeRoot, e); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(corpusRoot, "codex", "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corpusRoot, "codex", "session", "0001.md"), []byte("raw spindle context and evidence"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Search(Options{CorpusRoot: corpusRoot, EpisodeRoot: episodeRoot, Scope: "spindle", Query: "durable context", Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 1 || results[0].Kind != "episode" {
		t.Fatalf("results = %#v", results)
	}
	text, err := Read(Options{EpisodeRoot: episodeRoot}, results[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Build wake search") {
		t.Fatalf("read = %q", text)
	}
	if _, err := Read(Options{EpisodeRoot: episodeRoot}, "episode:../../etc/0001"); err == nil {
		t.Fatal("Read accepted path traversal")
	}
}

func TestReadExpandsEvidenceCitation(t *testing.T) {
	root := t.TempDir()
	corpusRoot := filepath.Join(root, "corpus")
	chunk := corpus.Chunk{Source: "codex", Session: "01a07de8-ae9d-7291-a230-df7f5da2d1cd", Index: 1, Items: []corpus.Item{{ID: "codex:01a07de8#L315", Line: 315, Role: corpus.RoleAssistant, Text: "Recovered design context."}}}
	chunk.Start, chunk.End = chunk.Items[0].Time, chunk.Items[0].Time
	if _, err := chunk.Write(corpusRoot); err != nil {
		t.Fatal(err)
	}
	text, err := Read(Options{CorpusRoot: corpusRoot}, "codex:01a07de8#L315")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Recovered design context.") {
		t.Fatalf("read = %q", text)
	}
}

func TestWakePrioritizesEpisodeOverHigherScoringRawMatch(t *testing.T) {
	root := t.TempDir()
	episodeRoot, corpusRoot := filepath.Join(root, "episodes"), filepath.Join(root, "corpus")
	e := episode.Episode{ID: "ep-codex-session-0001", Source: "codex", Session: "session", Chunk: 1, Scope: "spindle", Title: "Context"}
	if err := episode.Write(episodeRoot, e); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(corpusRoot, "codex", "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corpusRoot, "codex", "session", "0001.md"), []byte("spindle spindle spindle"), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := Wake(Options{CorpusRoot: corpusRoot, EpisodeRoot: episodeRoot, Scope: "spindle", Query: "spindle", Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Kind != "episode" {
		t.Fatalf("wake results = %#v", results)
	}
}

func TestSearchFiltersToOneKnownSession(t *testing.T) {
	root := t.TempDir()
	corpusRoot := filepath.Join(root, "corpus")
	for _, session := range []string{"keep", "other"} {
		chunk := corpus.Chunk{Source: "codex", Session: session, Index: 1, Items: []corpus.Item{{ID: "codex:" + session + "#L1", Line: 1, Role: corpus.RoleUser, Text: "Find relevant examples."}}}
		if _, err := chunk.Write(corpusRoot); err != nil {
			t.Fatal(err)
		}
	}
	results, err := Search(Options{CorpusRoot: corpusRoot, Source: "codex", Session: "keep", Query: "examples"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Ref != "transcript:codex/keep/0001" {
		t.Fatalf("results = %#v", results)
	}
	if _, err := Search(Options{CorpusRoot: corpusRoot, Session: "keep", Query: "examples"}); err == nil {
		t.Fatal("search accepted a session without its source")
	}
}

func TestRelatedReadsExplicitContinuationOnly(t *testing.T) {
	root := t.TempDir()
	e := episode.Episode{ID: "ep-codex-session-0001", Source: "codex", Session: "session", Chunk: 1, Scope: "spindle", Continues: []episode.Link{{Ref: "artifact:docs/design.md", Why: "Current contract."}}}
	if err := episode.Write(root, e); err != nil {
		t.Fatal(err)
	}
	links, err := Related(Options{EpisodeRoot: root}, "episode:codex/session/0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Ref != "artifact:docs/design.md" {
		t.Fatalf("links = %#v", links)
	}
}

func TestResumeUsesLatestSummaryAndSurfacesTrailingSourceOnlyRange(t *testing.T) {
	root := t.TempDir()
	first := episode.Episode{ID: "ep-codex-session-0001", Source: "codex", Session: "session", Chunk: 1, Scope: "spindle", Status: episode.StatusSummary, Title: "Earlier handoff", Continues: []episode.Link{{Ref: "artifact:docs/design.md", Why: "Current contract."}}}
	if err := episode.Write(root, first); err != nil {
		t.Fatal(err)
	}
	fallback := episode.Episode{ID: "ep-codex-session-0002", Source: "codex", Session: "session", Chunk: 2, Scope: "spindle", Status: episode.StatusSourceOnly, Title: "Uncompacted terminal range", References: []episode.Reference{{Kind: "source", Ref: "transcript:codex/session/0002", Why: "Expand source.", Evidence: []string{"codex:session#L2"}}}}
	if err := episode.Write(root, fallback); err != nil {
		t.Fatal(err)
	}
	result, err := Resume(Options{EpisodeRoot: root}, "codex", "session")
	if err != nil {
		t.Fatal(err)
	}
	if result.Latest == nil || result.Latest.Ref != "episode:codex/session/0001" {
		t.Fatalf("latest = %#v", result.Latest)
	}
	if len(result.Continues) != 1 || result.Continues[0].Ref != "artifact:docs/design.md" {
		t.Fatalf("continues = %#v", result.Continues)
	}
	if len(result.SourceOnly) != 1 || result.SourceOnly[0].Ref != "transcript:codex/session/0002" {
		t.Fatalf("source-only = %#v", result.SourceOnly)
	}
}

func TestResumeReturnsSourceOnlyWhenNoSummaryExists(t *testing.T) {
	root := t.TempDir()
	fallback := episode.Episode{ID: "ep-codex-session-0001", Source: "codex", Session: "session", Chunk: 1, Scope: "spindle", Status: episode.StatusSourceOnly, Title: "Uncompacted range", Continues: []episode.Link{{Ref: "artifact:docs/design.md", Why: "Current contract."}}, References: []episode.Reference{{Kind: "source", Ref: "transcript:codex/session/0001", Why: "Expand source.", Evidence: []string{"codex:session#L1"}}}}
	if err := episode.Write(root, fallback); err != nil {
		t.Fatal(err)
	}
	result, err := Resume(Options{EpisodeRoot: root}, "codex", "session")
	if err != nil {
		t.Fatal(err)
	}
	if result.Latest != nil || len(result.SourceOnly) != 1 || len(result.Continues) != 1 || result.Continues[0].Ref != "artifact:docs/design.md" {
		t.Fatalf("resume = %#v", result)
	}
}

func TestResumeFallsBackToUnprojectedCorpusSession(t *testing.T) {
	root := t.TempDir()
	corpusRoot := filepath.Join(root, "corpus")
	for _, index := range []int{1, 2} {
		chunk := corpus.Chunk{Source: "codex", Session: "session", Index: index, Items: []corpus.Item{{ID: "codex:session#L1", Line: 1, Role: corpus.RoleUser, Text: "Raw context."}}}
		if _, err := chunk.Write(corpusRoot); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Resume(Options{CorpusRoot: corpusRoot, EpisodeRoot: filepath.Join(root, "episodes")}, "codex", "session")
	if err != nil {
		t.Fatal(err)
	}
	if result.Latest != nil || len(result.SourceOnly) != 2 {
		t.Fatalf("resume = %#v", result)
	}
	if result.SourceOnly[0].Ref != "transcript:codex/session/0001" || result.SourceOnly[1].Ref != "transcript:codex/session/0002" {
		t.Fatalf("source-only = %#v", result.SourceOnly)
	}
}

// TestResumeReportsGapAfterLatestEpisode covers the freshness contract: a
// final chunk that grew after projection and a chunk never projected both
// land in the raw tail, and the marks bound the gap.
func TestResumeReportsGapAfterLatestEpisode(t *testing.T) {
	root := t.TempDir()
	corpusRoot, episodeRoot := filepath.Join(root, "corpus"), filepath.Join(root, "episodes")
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	chunk := func(index int, texts ...string) corpus.Chunk {
		c := corpus.Chunk{Source: "codex", Session: "session", Index: index}
		for i, text := range texts {
			c.Items = append(c.Items, corpus.Item{ID: corpus.ItemID("codex", "session", index*10+i), Line: index*10 + i, Time: t0.Add(time.Duration(index*10+i) * time.Minute), Role: corpus.RoleUser, Text: text})
		}
		c.Start, c.End = c.Items[0].Time, c.Items[len(c.Items)-1].Time
		return c
	}
	write := func(c corpus.Chunk) string {
		p, err := c.Write(corpusRoot)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Project chunks 1 and 2 as they were, then let chunk 2 grow and chunk 3 appear.
	for _, c := range []corpus.Chunk{chunk(1, "one"), chunk(2, "two")} {
		raw, _ := os.ReadFile(write(c))
		e := episode.Episode{ID: fmt.Sprintf("ep-codex-session-%04d", c.Index), Source: "codex", Session: "session", Chunk: c.Index, Scope: "spindle", Status: episode.StatusSummary, Title: fmt.Sprintf("Range %d", c.Index), End: c.End, SourceHash: sourceDigest(raw), Purpose: episode.Claim{Text: "work", Evidence: []string{c.Items[0].ID}}}
		if err := episode.Write(episodeRoot, e); err != nil {
			t.Fatal(err)
		}
	}
	write(chunk(2, "two", "two more"))
	grown := chunk(3, "three")
	write(grown)

	result, err := Resume(Options{CorpusRoot: corpusRoot, EpisodeRoot: episodeRoot}, "codex", "session")
	if err != nil {
		t.Fatal(err)
	}
	if result.Latest == nil || result.Latest.Ref != "episode:codex/session/0002" {
		t.Fatalf("latest = %#v", result.Latest)
	}
	if result.Projected == nil || result.Projected.Chunk != 2 || result.Captured == nil || result.Captured.Chunk != 3 || !result.Captured.End.Equal(grown.End) {
		t.Fatalf("projected = %#v, captured = %#v", result.Projected, result.Captured)
	}
	if len(result.SourceOnly) != 2 || result.SourceOnly[0].Ref != "transcript:codex/session/0002" || result.SourceOnly[1].Ref != "transcript:codex/session/0003" {
		t.Fatalf("raw tail = %#v", result.SourceOnly)
	}
	if !strings.Contains(result.SourceOnly[0].Excerpt, "Changed since projection") {
		t.Fatalf("grown chunk reason = %q", result.SourceOnly[0].Excerpt)
	}
}
