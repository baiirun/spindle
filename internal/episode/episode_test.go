package episode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

func TestProjectWritesEvidenceLinkedEpisodeAndContinuation(t *testing.T) {
	corpusRoot, outRoot := t.TempDir(), t.TempDir()
	start := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= 2; i++ {
		item := corpus.Item{ID: corpus.ItemID("codex", "session-123", i), Line: i, Time: start.Add(time.Duration(i) * time.Minute), Role: corpus.RoleUser, Text: "continue the spindle work"}
		chunk := corpus.Chunk{Source: "codex", Session: "session-123", Cwd: "/work/Spindle", Index: i, Start: item.Time, End: item.Time, Items: []corpus.Item{item}}
		if _, err := chunk.Write(corpusRoot); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	extract := func(_ context.Context, _ string, _ string, prompt string, _ string, out any) (llm.Result, error) {
		calls++
		evidence := "codex:session-#L1"
		if strings.Contains(prompt, "#L2") {
			evidence = "codex:session-#L2"
		}
		b, _ := json.Marshal(map[string]any{
			"title": "Continue Spindle", "purpose": map[string]any{"text": "Continue the Spindle work.", "evidence": []string{evidence}},
			"observations": []map[string]any{{"text": "The user asked to continue.", "evidence": []string{evidence, "not-in-source"}}},
			"outputs":      []any{}, "open_threads": []map[string]any{{"text": "Decide the next slice.", "evidence": []string{evidence}}}, "references": []any{},
		})
		_ = json.Unmarshal(b, out)
		return llm.Result{}, nil
	}

	episodes, err := Project(context.Background(), Options{
		CorpusRoot: corpusRoot, OutRoot: outRoot, Source: "codex", Session: "session-123", extract: extract,
		Continues: []Link{{Ref: "artifact:docs/design.md", Why: "Design contract."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(episodes))
	}
	if got := episodes[0].Observations[0].Evidence; len(got) != 1 || got[0] != "codex:session-#L1" {
		t.Fatalf("evidence = %#v", got)
	}
	if len(episodes[0].Continues) != 1 || episodes[0].Continues[0].Ref != "artifact:docs/design.md" {
		t.Fatalf("first links = %#v", episodes[0].Continues)
	}
	if len(episodes[1].Continues) != 1 || !strings.HasPrefix(episodes[1].Continues[0].Ref, "episode:codex/session-123/0001") {
		t.Fatalf("second links = %#v", episodes[1].Continues)
	}
	text, err := os.ReadFile(filepath.Join(outRoot, "codex", "session-123", "0001.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "[codex:session-#L1]") || strings.Contains(string(text), "not-in-source") {
		t.Fatalf("rendered episode did not preserve validated evidence:\n%s", text)
	}
	if _, err := Project(context.Background(), Options{
		CorpusRoot: corpusRoot, OutRoot: outRoot, Source: "codex", Session: "session-123", extract: extract,
		Continues: []Link{{Ref: "artifact:docs/design.md", Why: "Design contract."}},
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("extractor calls = %d, want 2 after cache hit", calls)
	}
	text, err = os.ReadFile(filepath.Join(outRoot, "codex", "session-123", "0001.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(text), "artifact:docs/design.md"); got != 2 {
		t.Fatalf("continuation link count = %d, want 2 (frontmatter and body)", got)
	}
	if !strings.Contains(string(text), "The user asked to continue.") {
		t.Fatal("cache hit rewrote episode without its observations")
	}
}

func TestObserverSchemaHasValidRequiredShape(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(observerSchema()), &schema); err != nil {
		t.Fatal(err)
	}
	if got, ok := schema["required"].([]any); !ok || len(got) != 6 {
		t.Fatalf("top-level required = %#v", schema["required"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema["properties"])
	}
	for _, name := range []string{"purpose", "observations", "references"} {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("property %s = %#v", name, properties[name])
		}
		if _, ok := property["required"].([]any); !ok && name == "purpose" {
			t.Fatalf("purpose required = %#v", property["required"])
		}
	}
}

func TestProjectRetriesCitationEmptyObserverResult(t *testing.T) {
	corpusRoot, outRoot := t.TempDir(), t.TempDir()
	item := corpus.Item{ID: corpus.ItemID("codex", "session-123", 1), Line: 1, Time: time.Now(), Role: corpus.RoleUser, Text: "Continue the Eldspire rules."}
	chunk := corpus.Chunk{Source: "codex", Session: "session-123", Cwd: "/work/Eldspire", Index: 1, Start: item.Time, End: item.Time, Items: []corpus.Item{item}}
	if _, err := chunk.Write(corpusRoot); err != nil {
		t.Fatal(err)
	}
	calls := 0
	extract := func(_ context.Context, _ string, _ string, _ string, _ string, out any) (llm.Result, error) {
		calls++
		payload := map[string]any{"title": "Eldspire rules", "purpose": map[string]any{}, "observations": []any{}, "outputs": []any{}, "open_threads": []any{}, "references": []any{}}
		if calls == 2 {
			payload["purpose"] = map[string]any{"text": "Continue the Eldspire rules.", "evidence": []string{item.ID}}
		}
		b, _ := json.Marshal(payload)
		_ = json.Unmarshal(b, out)
		return llm.Result{}, nil
	}
	episodes, err := Project(context.Background(), Options{CorpusRoot: corpusRoot, OutRoot: outRoot, Source: "codex", Session: "session-123", extract: extract})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || episodes[0].Purpose.Text == "" {
		t.Fatalf("calls = %d, episode = %#v", calls, episodes[0])
	}
}

func TestProjectLeavesSourcePointerWhenObserverStaysEmpty(t *testing.T) {
	corpusRoot, outRoot := t.TempDir(), t.TempDir()
	item := corpus.Item{ID: corpus.ItemID("codex", "session-123", 1), Line: 1, Time: time.Now(), Role: corpus.RoleUser, Text: "Continue the Eldspire rules."}
	chunk := corpus.Chunk{Source: "codex", Session: "session-123", Cwd: "/work/Eldspire", Index: 1, Start: item.Time, End: item.Time, Items: []corpus.Item{item}}
	if _, err := chunk.Write(corpusRoot); err != nil {
		t.Fatal(err)
	}
	calls := 0
	extract := func(_ context.Context, _ string, _ string, _ string, _ string, out any) (llm.Result, error) {
		calls++
		b, _ := json.Marshal(map[string]any{"title": "Eldspire rules", "purpose": map[string]any{}, "observations": []any{}, "outputs": []any{}, "open_threads": []any{}, "references": []any{}})
		_ = json.Unmarshal(b, out)
		return llm.Result{}, nil
	}
	episodes, err := Project(context.Background(), Options{CorpusRoot: corpusRoot, OutRoot: outRoot, Source: "codex", Session: "session-123", extract: extract})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(episodes[0].References) != 1 || episodes[0].References[0].Ref != "transcript:codex/session-123/0001" {
		t.Fatalf("calls = %d, episode = %#v", calls, episodes[0])
	}
	if episodes[0].Status != StatusSourceOnly {
		t.Fatalf("status = %q, want %q", episodes[0].Status, StatusSourceOnly)
	}
	persisted, err := ReadPath(Path(outRoot, "codex", "session-123", 1))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusSourceOnly {
		t.Fatalf("persisted status = %q, want %q", persisted.Status, StatusSourceOnly)
	}
	if _, err := Project(context.Background(), Options{CorpusRoot: corpusRoot, OutRoot: outRoot, Source: "codex", Session: "session-123", extract: extract}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("fallback should be cached, calls = %d", calls)
	}
}

// TestValidEvidenceAcceptsShortenedCitations covers the main cause of empty
// episodes: models citing "L2934" or "01a07de8#L2934" instead of the full ID.
func TestValidEvidenceAcceptsShortenedCitations(t *testing.T) {
	known := map[string]string{
		"codex:01a07de8#L2934": "codex:01a07de8#L2934", "L2934": "codex:01a07de8#L2934",
		"codex:01a07de8#L2942": "codex:01a07de8#L2942", "L2942": "codex:01a07de8#L2942",
	}
	got := validEvidence([]string{"L2934", "01a07de8#L2942", "codex:01a07de8#L2934", "L9999", "nonsense"}, known)
	want := []string{"codex:01a07de8#L2934", "codex:01a07de8#L2942"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("validEvidence = %v, want %v", got, want)
	}
}
