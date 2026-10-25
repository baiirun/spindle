package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	e.h = Handler{Home: t.TempDir(), Agent: "claude", RecallWait: time.Second,
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
	if o.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "memory_write") {
		t.Fatalf("output = %+v", o)
	}
}

func TestPromptRecallLocalAndStop(t *testing.T) {
	e := setup(t)
	p := memory.At(e.h.Home, e.root)
	p.Apply([]memory.Op{{Op: "write", Name: "event-log-store", Type: "project", Description: "Event log lives in Postgres", Body: "Postgres."}}, "agent", "", time.Now())
	e.h.LocalRecall = true
	o := e.run(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "where is the event log stored?"})
	if !strings.Contains(o.HookSpecificOutput.AdditionalContext, "Memory: event-log-store.md") {
		t.Fatalf("recall = %+v", o)
	}
	e.run(t, map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": "/t/s.jsonl"})
	if len(e.spawned) != 1 || e.spawned[0][0] != "worker" {
		t.Fatalf("spawned = %v", e.spawned)
	}
	jobs, _ := filepath.Glob(filepath.Join(e.h.Home, "queue", "*.json"))
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v", jobs)
	}
}

func TestPromptRecallAsync(t *testing.T) {
	e := setup(t)
	p := memory.At(e.h.Home, e.root)
	p.Apply([]memory.Op{{Op: "write", Name: "event-log-store", Type: "project", Description: "Event log lives in Postgres", Body: "Postgres."}}, "agent", "", time.Now())
	e.h.RecallWait = 100 * time.Millisecond
	o := e.run(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "where is the event log stored?"})
	if o.HookSpecificOutput.AdditionalContext != "" || len(e.spawned) != 1 || e.spawned[0][0] != "select" {
		t.Fatalf("output %+v, spawned %v", o, e.spawned)
	}
	// The selector finishes after the prompt went in; the next tool call carries it.
	args := e.spawned[0]
	p.RunSelect(context.Background(), "s", args[6], nil, true)
	o = e.run(t, map[string]any{"hook_event_name": "PostToolUse", "session_id": "s"})
	if !strings.Contains(o.HookSpecificOutput.AdditionalContext, "event-log-store.md") || o.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("PostToolUse = %+v", o)
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
		for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "PreCompact", "SessionEnd"} {
			if h := cfg.Hooks[ev]; len(h) != 1 || h[0].Hooks[0]["command"] != "'/opt/my bin/spin' hook --agent "+agent {
				t.Fatalf("%s %s = %+v", agent, ev, h)
			}
		}
		if cfg.Hooks["Stop"][0].Hooks[0]["async"] != true || (agent == "claude") != (cfg.AutoMemory != nil && !*cfg.AutoMemory) {
			t.Fatalf("%s config = %+v", agent, cfg)
		}
	}
}
