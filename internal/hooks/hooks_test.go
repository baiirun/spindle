package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindle/internal/memory"
)

type env struct {
	h       Handler
	root    string
	spawned [][]string
}

func setup(t *testing.T) *env {
	t.Helper()
	t.Setenv(memory.ProjectEnv, "")
	t.Setenv(memory.InnerEnv, "")
	e := &env{root: t.TempDir()}
	os.Mkdir(filepath.Join(e.root, ".git"), 0o755)
	home := t.TempDir()
	memory.Store(home, "proj").Link(e.root)
	e.h = Handler{Home: home, Agent: "claude",
		Spawn: func(args ...string) error { e.spawned = append(e.spawned, args); return nil }}
	return e
}

func (e *env) run(t *testing.T, ev map[string]any) Output {
	t.Helper()
	ev["cwd"] = e.root
	b, _ := json.Marshal(ev)
	var out bytes.Buffer
	if err := e.h.Run(context.Background(), bytes.NewReader(b), &out); err != nil {
		t.Fatal(err)
	}
	var o Output
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &o); err != nil {
			t.Fatalf("bad output %q", out.String())
		}
	}
	return o
}

func TestSessionStart(t *testing.T) {
	e := setup(t)
	o := e.run(t, map[string]any{"hook_event_name": "SessionStart", "source": "startup", "session_id": "s"})
	if o.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "memory_append") {
		t.Fatalf("output = %+v", o)
	}
}

func TestInnerIgnored(t *testing.T) {
	e := setup(t)
	t.Setenv(memory.InnerEnv, "1")
	if o := e.run(t, map[string]any{"hook_event_name": "SessionStart"}); o.HookSpecificOutput.AdditionalContext != "" || len(e.spawned) != 0 {
		t.Fatal("hook ran inside spindle's own model call")
	}
}

func TestInstallConfig(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		out, err := InstallConfig(agent, "/opt/my bin/spin")
		if err != nil {
			t.Fatal(err)
		}
		i, j := strings.Index(out, "{"), strings.Index(out, "\n}\n")
		var cfg struct {
			AutoMemory *bool                                         `json:"autoMemoryEnabled"`
			Hooks      map[string][]struct{ Hooks []map[string]any } `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(out[i:j+2]), &cfg); err != nil {
			t.Fatalf("%s: %v\n%s", agent, err, out)
		}
		if len(cfg.Hooks) != 4 {
			t.Fatalf("%s: hooks %v; recall is gone, so only start and the three that queue ingest", agent, cfg.Hooks)
		}
		for _, ev := range []string{"SessionStart", "Stop", "PreCompact", "SessionEnd"} {
			if h := cfg.Hooks[ev]; len(h) != 1 || h[0].Hooks[0]["command"] != "'/opt/my bin/spin' hook --agent "+agent {
				t.Fatalf("%s %s = %+v", agent, ev, h)
			}
		}
		if cfg.Hooks["Stop"][0].Hooks[0]["async"] != true || (agent == "claude") != (cfg.AutoMemory != nil && !*cfg.AutoMemory) {
			t.Fatalf("%s config = %+v", agent, cfg)
		}
	}
}

// OpenCode's plugin is a file, not hooks to merge: it must carry the binary
// as a JS string and hand every event to `spin hook --agent opencode`.
func TestInstallOpencode(t *testing.T) {
	out, err := InstallConfig("opencode", `/opt/my "bin"/spin`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "__SPIN__") || !strings.Contains(out, `const SPIN = "/opt/my \"bin\"/spin"`) {
		t.Fatalf("binary not filled in:\n%s", out)
	}
	for _, s := range []string{`"hook", "--agent", "opencode"`, `"mcp", "--agent", "opencode"`,
		`"SessionStart"`, `"Stop"`, `"PreCompact"`} {
		if !strings.Contains(out, s) {
			t.Errorf("plugin lacks %s", s)
		}
	}
	for _, s := range []string{`"UserPromptSubmit"`, `"PostToolUse"`} {
		if strings.Contains(out, s) {
			t.Errorf("plugin still sends %s; recall is gone", s)
		}
	}
}

// Invariant 16: a hook never fails a session. Bad input is an error for
// spin to log (it still exits 0); odd events and missing fields do nothing.
func TestOddInputDoesNothing(t *testing.T) {
	e := setup(t)
	var out bytes.Buffer
	if err := e.h.Run(context.Background(), strings.NewReader("not json"), &out); err == nil || out.Len() != 0 {
		t.Fatalf("bad input: err %v, output %q", err, out.String())
	}
	for _, ev := range []map[string]any{
		{"hook_event_name": "Notification", "session_id": "s"},
		{"hook_event_name": "Stop", "session_id": "s"},                                      // no transcript
		{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "hello there"}, // v1 installs still send it
		{"hook_event_name": "PostToolUse", "session_id": "never-seen"},
	} {
		if o := e.run(t, ev); o.HookSpecificOutput.AdditionalContext != "" {
			t.Errorf("%v: output %+v", ev, o)
		}
	}
	if len(e.spawned) != 0 {
		t.Errorf("spawned %v", e.spawned)
	}
}

// Codex sends the same fields; with no --agent the transcript says which tool it is.
func TestCodexSessionQueuesAsCodex(t *testing.T) {
	e := setup(t)
	e.h.Agent = ""
	tp := filepath.Join(t.TempDir(), "rollout.jsonl")
	os.WriteFile(tp, []byte(`{"timestamp":"2026-10-25T04:00:00Z","type":"session_meta","payload":{"id":"019e-codex","cwd":"`+e.root+`","source":"cli"}}`+"\n"), 0o644)
	e.run(t, map[string]any{"hook_event_name": "Stop", "session_id": "019e-codex", "transcript_path": tp})
	jobs, _ := filepath.Glob(filepath.Join(e.h.Home, "queue", "*.json"))
	if len(jobs) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	var j memory.Job
	b, _ := os.ReadFile(jobs[0])
	json.Unmarshal(b, &j)
	if j.Agent != "codex" || j.Project != "proj" || j.Transcript != tp {
		t.Errorf("job %+v", j)
	}
}
