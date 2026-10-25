package model

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stand-in writes a fake CLI named bin onto PATH. It records its arguments
// (one per line) and stdin, prints out and exits with code.
func standIn(t *testing.T, bin, out string, code int) (args func() []string, stdin func() string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, inFile, outFile := filepath.Join(dir, "args"), filepath.Join(dir, "stdin"), filepath.Join(dir, "out")
	os.WriteFile(outFile, []byte(out), 0o644)
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argsFile + "\n/bin/cat > " + inFile + "\n/bin/cat " + outFile + "\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
			b, _ := os.ReadFile(argsFile)
			return strings.Split(strings.TrimSpace(string(b)), "\n")
		}, func() string {
			b, _ := os.ReadFile(inFile)
			return string(b)
		}
}

func hasArgs(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j, w := range want {
			if args[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Invariant 19: side calls run with tools, MCP, settings and saved sessions off.
func TestClaudeCallIsLockedDown(t *testing.T) {
	args, stdin := standIn(t, "claude", `{"type":"result","result":"","structured_output":{"selected_memories":["a.md"]}}`, 0)
	got, err := Claude{Model: "sonnet"}.Complete(context.Background(), "SYSTEM", "PROMPT", `{"type":"object"}`)
	if err != nil || got != `{"selected_memories":["a.md"]}` {
		t.Fatalf("answer %q, %v", got, err)
	}
	a := args()
	for _, want := range [][]string{{"-p"}, {"--model", "sonnet"}, {"--system-prompt", "SYSTEM"}, {"--tools", ""}, {"--no-session-persistence"}, {"--strict-mcp-config"}, {"--setting-sources", ""}, {"--json-schema", `{"type":"object"}`}} {
		if !hasArgs(a, want...) {
			t.Errorf("claude args lack %q: %q", want, a)
		}
	}
	if stdin() != "PROMPT" {
		t.Errorf("stdin %q", stdin())
	}
}

func TestClaudeAnswers(t *testing.T) {
	for name, c := range map[string]struct {
		out, want string
		fails     bool
	}{
		"structured":      {out: `{"type":"result","result":"x","structured_output":{"a":1}}`, want: `{"a":1}`},
		"null structured": {out: `{"type":"result","result":"Here: {\"a\":2} done","structured_output":null}`, want: `{"a":2}`},
		"error result":    {out: `{"type":"result","result":"Credit balance is too low","is_error":true}`, fails: true},
		"not json":        {out: `Not logged in`, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			standIn(t, "claude", c.out, 0)
			got, err := Claude{Model: "sonnet"}.Complete(context.Background(), "s", "p", `{}`)
			if c.fails != (err != nil) || (!c.fails && got != c.want) {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
	standIn(t, "claude", "", 1)
	if _, err := (Claude{Model: "sonnet"}).Complete(context.Background(), "s", "p", ""); err == nil {
		t.Error("exit 1: want error")
	}
}

func TestCodexCallIsReadOnlyAndEphemeral(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started"}`,
		`{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"draft"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"{\"ops\":[]}"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	args, stdin := standIn(t, "codex", stream, 0)
	got, err := Codex{Model: "gpt-6-luna", Effort: "low"}.Complete(context.Background(), "SYSTEM", "PROMPT", `{"type":"object"}`)
	if err != nil || got != `{"ops":[]}` {
		t.Fatalf("answer %q, %v", got, err)
	}
	a := args()
	for _, want := range [][]string{{"exec"}, {"--ephemeral"}, {"--sandbox", "read-only"}, {"--json"}, {"--model", "gpt-6-luna"}, {"-c", `model_reasoning_effort="low"`}, {"--output-schema"}} {
		if !hasArgs(a, want...) {
			t.Errorf("codex args lack %q: %q", want, a)
		}
	}
	if in := stdin(); !strings.HasPrefix(in, "<instructions>\nSYSTEM\n</instructions>") || !strings.HasSuffix(in, "PROMPT") {
		t.Errorf("stdin %q", in)
	}
	standIn(t, "codex", `{"type":"turn.completed"}`, 0)
	if _, err := (Codex{Model: "m", Effort: "low"}).Complete(context.Background(), "s", "p", ""); err == nil {
		t.Error("no agent message: want error")
	}
}

// A missing codex falls back to claude, as on a machine without Codex.
func TestFallbackWhenCodexIsMissing(t *testing.T) {
	standIn(t, "claude", `{"type":"result","result":"","structured_output":{"ok":true}}`, 0)
	t.Setenv("PATH", filepath.Dir(lookPath(t, "claude")))
	t.Setenv(BackendEnv, "")
	got, err := For("claude", Extract).Complete(context.Background(), "s", "p", `{}`)
	if err != nil || got != `{"ok":true}` {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Once the caller gives up, the fallback doesn't start another model.
func TestFallbackStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tried := 0
	slow := Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		tried++
		cancel()
		<-ctx.Done()
		return "", ctx.Err()
	})
	start := time.Now()
	if _, err := (Fallback{slow, slow}).Complete(ctx, "", "", ""); err == nil || tried != 1 || time.Since(start) > time.Second {
		t.Errorf("err %v after %d tries", err, tried)
	}
}

func lookPath(t *testing.T, bin string) string {
	t.Helper()
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, bin)); err == nil {
			return filepath.Join(d, bin)
		}
	}
	t.Fatalf("%s not on PATH", bin)
	return ""
}

func TestJSONObject(t *testing.T) {
	for in, want := range map[string]string{
		"```json\n{\"a\":{\"b\":1}}\n```": `{"a":{"b":1}}`,
		`{"a":1}`:                         `{"a":1}`,
		"no object here":                  "no object here",
	} {
		if got := JSONObject(in); got != want {
			t.Errorf("JSONObject(%q) = %q", in, got)
		}
	}
}
