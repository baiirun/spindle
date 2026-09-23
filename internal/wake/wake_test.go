package wake

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
