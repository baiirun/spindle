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
	t.Setenv("SPINDLE_HOME", "")
	srcs, err := Load(home)
	if err != nil || len(srcs) != 3 || srcs[0].Kind() != ClaudeCode || srcs[1].Kind() != Codex || srcs[2].Kind() != OpenCode {
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

// Each kind reads its tool's folder by default, following the same
// environment the tool (or, for OpenCode, spindle's plugin) does, and tags
// what it finds with the agent that picks the side models.
func TestNewKinds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]struct {
		spec  Spec
		env   map[string]string
		dir   string
		agent string
	}{
		"claude-code":                   {spec: Spec{Kind: ClaudeCode}, dir: filepath.Join(home, ".claude", "projects"), agent: "claude"},
		"claude-code CLAUDE_CONFIG_DIR": {spec: Spec{Kind: ClaudeCode}, env: map[string]string{"CLAUDE_CONFIG_DIR": "/c"}, dir: "/c/projects", agent: "claude"},
		"codex":                         {spec: Spec{Kind: Codex}, dir: filepath.Join(home, ".codex", "sessions"), agent: "codex"},
		"codex CODEX_HOME":              {spec: Spec{Kind: Codex}, env: map[string]string{"CODEX_HOME": "/x"}, dir: "/x/sessions", agent: "codex"},
		"opencode":                      {spec: Spec{Kind: OpenCode}, dir: filepath.Join(home, ".spindle", "opencode"), agent: "opencode"},
		"opencode SPINDLE_HOME":         {spec: Spec{Kind: OpenCode}, env: map[string]string{"SPINDLE_HOME": "/s"}, dir: "/s/opencode", agent: "opencode"},
		"opencode --dir":                {spec: Spec{Kind: OpenCode, Dir: "~/oc"}, env: map[string]string{"SPINDLE_HOME": "/s"}, dir: filepath.Join(home, "oc"), agent: "opencode"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "SPINDLE_HOME"} {
				t.Setenv(k, c.env[k])
			}
			src, err := New(c.spec)
			if err != nil {
				t.Fatal(err)
			}
			f := src.(Files)
			if f.Kind() != c.spec.Kind || f.Dir() != c.dir || f.agent != c.agent {
				t.Errorf("kind %s dir %s agent %s, want %s %s %s", f.Kind(), f.Dir(), f.agent, c.spec.Kind, c.dir, c.agent)
			}
		})
	}
}

// The OpenCode source lists the plugin's files, and reading one through
// source.Read gives an OpenCode session; a subagent's file is listed but
// flagged, so the sweep and extraction skip it.
func TestOpenCodeSessions(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	files := map[string]struct {
		lines    string
		subagent bool
	}{
		"ses_main.jsonl": {lines: `{"type":"session","agent":"opencode","id":"ses_main","cwd":"/w"}
{"type":"message","mid":"msg_1","role":"user","time":"2026-10-25T04:00:00Z","text":"Use Postgres."}
`},
		"ses_task.jsonl": {lines: `{"type":"session","agent":"opencode","id":"ses_task","cwd":"/w","parentID":"ses_main"}
{"type":"message","mid":"msg_2","role":"user","time":"2026-10-25T04:00:00Z","text":"Find the config."}
`, subagent: true},
	}
	for name, f := range files {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(f.lines), 0o644)
		os.Chtimes(p, now.Add(-time.Hour), now.Add(-time.Hour))
	}
	src, err := New(Spec{Kind: OpenCode, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := src.List(context.Background(), now.Add(-2*time.Hour), now)
	if err != nil || len(refs) != len(files) {
		t.Fatalf("refs %+v %v", refs, err)
	}
	for _, r := range refs {
		f := files[filepath.Base(r.Loc)]
		if r.Source != OpenCode || r.Agent != "opencode" {
			t.Errorf("ref %+v", r)
		}
		s, err := Read(context.Background(), r.Loc)
		if err != nil || s.Agent != "opencode" || s.Subagent != f.subagent || len(s.Messages) != 1 {
			t.Errorf("%s: session %+v %v", r.Loc, s, err)
		}
	}
}
