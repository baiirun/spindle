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

func setup(t *testing.T) (Handler, string, *int) {
	t.Helper()
	t.Setenv(memory.ProjectEnv, "")
	t.Setenv(memory.InnerEnv, "")
	home, root := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	p := memory.ProjectAt(home, root)
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	spawned := 0
	return Handler{Home: home, Spawn: func() error { spawned++; return nil }}, root, &spawned
}

func run(t *testing.T, h Handler, in map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(in)
	var out bytes.Buffer
	if err := h.Run(context.Background(), bytes.NewReader(b), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func jobs(t *testing.T, home string) []memory.Job {
	paths, _ := filepath.Glob(filepath.Join(home, "queue", "*.json"))
	var out []memory.Job
	for _, p := range paths {
		var j memory.Job
		b, _ := os.ReadFile(p)
		json.Unmarshal(b, &j)
		out = append(out, j)
	}
	return out
}

func TestStopQueuesAndSpawns(t *testing.T) {
	h, root, spawned := setup(t)
	out := run(t, h, map[string]any{"hook_event_name": "Stop", "session_id": "s1", "cwd": filepath.Join(root), "transcript_path": "/t/s1.jsonl"})
	if out != "" || *spawned != 1 {
		t.Fatalf("out %q, spawned %d", out, *spawned)
	}
	js := jobs(t, h.Home)
	if len(js) != 1 || js[0].Transcript != "/t/s1.jsonl" || js[0].Root != root || js[0].Force {
		t.Fatalf("jobs = %+v", js)
	}
	run(t, h, map[string]any{"hook_event_name": "PreCompact", "session_id": "s1", "cwd": root, "transcript_path": "/t/s1.jsonl", "trigger": "auto"})
	if js = jobs(t, h.Home); len(js) != 2 || !js[1].Force {
		t.Fatalf("PreCompact job not forced: %+v", js)
	}
}

func TestSessionStartAndPromptInject(t *testing.T) {
	h, root, _ := setup(t)
	if out := run(t, h, map[string]any{"hook_event_name": "SessionStart", "source": "startup", "session_id": "s2", "cwd": root}); out != "" {
		t.Fatalf("empty memory injected %q", out)
	}
	p := memory.ProjectAt(h.Home, root)
	rec := memory.Record{}
	rec.Apply([]memory.Op{{Op: "add", Kind: "decision", Key: "spin binary name", Text: "The CLI is called spin.", Who: "user"}}, rec.AsOf, nil, "tool")
	p.SaveRecord(rec)

	var o Output
	if err := json.Unmarshal([]byte(run(t, h, map[string]any{"hook_event_name": "SessionStart", "source": "compact", "session_id": "s2", "cwd": root})), &o); err != nil {
		t.Fatal(err)
	}
	if o.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "spin binary name") {
		t.Fatalf("SessionStart output = %+v", o)
	}
	if js := jobs(t, h.Home); len(js) == 0 || !js[len(js)-1].Flush || js[len(js)-1].Transcript != "" {
		t.Fatalf("SessionStart did not queue a flush: %+v", js)
	}
	o = Output{}
	json.Unmarshal([]byte(run(t, h, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "what is the binary called?", "session_id": "s2", "cwd": root})), &o)
	if o.HookSpecificOutput.HookEventName != "UserPromptSubmit" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "The CLI is called spin.") {
		t.Fatalf("UserPromptSubmit output = %+v", o)
	}
}

func TestInnerProcessesAreIgnored(t *testing.T) {
	h, root, spawned := setup(t)
	t.Setenv(memory.InnerEnv, "1")
	run(t, h, map[string]any{"hook_event_name": "Stop", "cwd": root, "transcript_path": "/t/x.jsonl"})
	if *spawned != 0 || len(jobs(t, h.Home)) != 0 {
		t.Fatal("hook ran inside spindle's own worker")
	}
}
