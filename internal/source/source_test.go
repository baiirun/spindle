package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilesList(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	touch := func(rel string, at time.Time) string {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("{}\n"), 0o644)
		os.Chtimes(p, at, at)
		return p
	}
	old := touch("a/old.jsonl", now.Add(-3*time.Hour))
	inWindow := touch("a/b/new.jsonl", now.Add(-time.Hour))
	touch("a/s/subagents/agent.jsonl", now.Add(-time.Hour))
	touch("a/notes.txt", now.Add(-time.Hour))
	touch("a/live.jsonl", now) // still being written
	src, err := New(Spec{Kind: ClaudeCode, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := src.List(context.Background(), now.Add(-2*time.Hour), now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Loc != inWindow || refs[0].Agent != "claude" || refs[0].Source != ClaudeCode {
		t.Fatalf("refs %+v (old %s)", refs, old)
	}
	missing, _ := New(Spec{Kind: Codex, Dir: filepath.Join(dir, "none")})
	if refs, err := missing.List(context.Background(), time.Time{}, now); err != nil || len(refs) != 0 {
		t.Fatalf("a missing folder: %v %v", refs, err)
	}
}

func TestLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	srcs, err := Load(home)
	if err != nil || len(srcs) != 2 || srcs[0].Kind() != ClaudeCode || srcs[1].Kind() != Codex {
		t.Fatalf("defaults: %v %v", srcs, err)
	}
	if d := srcs[0].(Files).Dir(); d != filepath.Join(os.Getenv("HOME"), ".claude", "projects") {
		t.Errorf("claude dir %s", d)
	}
	os.WriteFile(ConfigPath(home), []byte(`{"sources":[{"kind":"codex","dir":"/x"},{"kind":"nope"}]}`), 0o644)
	srcs, err = Load(home)
	if err == nil || len(srcs) != 1 || srcs[0].(Files).Dir() != "/x" {
		t.Fatalf("config: %v %v", srcs, err)
	}
	os.WriteFile(ConfigPath(home), []byte(`{"sources":[]}`), 0o644)
	if srcs, err = Load(home); err != nil || len(srcs) != 0 {
		t.Fatalf("no sources: %v %v", srcs, err)
	}
}

func TestReadNeedsAPath(t *testing.T) {
	if _, err := Read(context.Background(), "cloud:session_1"); err == nil {
		t.Fatal("read a location no source has")
	}
}
