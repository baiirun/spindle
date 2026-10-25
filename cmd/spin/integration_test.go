package main

// Integration tests: the real spin binary, run the way Claude Code and Codex
// run it, against stand-in claude and codex CLIs on PATH. The stand-ins
// answer from files the test writes, can be made slow or broken, and log
// every call, so the tests need no network and no model.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var spinBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "spin-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	spinBin = filepath.Join(dir, "spin")
	if out, err := exec.Command("go", "build", "-o", spinBin, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build spin: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// standIn is shared by the fake claude and codex. It tells the role from the
// JSON schema it is given, logs "<cli> <role>", and prints <role>.<cli>.
const standIn = `#!/bin/sh
bin=$(/usr/bin/basename "$0")
d="$FAKE_DIR"
schema=""
prev=""
for a in "$@"; do
  [ "$prev" = "--json-schema" ] && schema="$a"
  [ "$prev" = "--output-schema" ] && schema=$(/bin/cat "$a")
  prev="$a"
done
/bin/cat > /dev/null
case "$schema" in
  *selected_memories*) role=select ;;
  *nothing_to_save*) role=read ;;
  *'"summary"'*) role=dream ;;
  *) role=write ;;
esac
echo "$bin $role" >> "$d/calls"
if [ -f "$d/$bin.fail" ]; then echo "$bin: broken" >&2; exit 1; fi
if [ -f "$d/$role.sleep" ]; then /bin/sleep "$(/bin/cat "$d/$role.sleep")"; fi
if [ -f "$d/recurse" ]; then
  printf '{"hook_event_name":"Stop","session_id":"inner","transcript_path":"%s","cwd":"/"}' "$d/inner.jsonl" | "$SPIN" hook --agent "$bin"
fi
/bin/cat "$d/$role.$bin"
`

type world struct {
	t     *testing.T
	home  string // SPINDLE_HOME
	work  string // the session's folder, linked to project "proj"
	fakes string // FAKE_DIR: answers, switches and the call log
	env   []string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, home: t.TempDir(), work: t.TempDir(), fakes: t.TempDir()}
	bin := t.TempDir()
	for _, cli := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, cli), []byte(standIn), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.env = []string{
		"SPINDLE_HOME=" + w.home, "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(),
		"PATH=" + bin + ":/usr/bin:/bin", "FAKE_DIR=" + w.fakes, "SPIN=" + spinBin,
		"SPINDLE_RECALL_WAIT=5",
	}
	w.spin(w.work, "", "project", "proj", "--link", w.work)
	return w
}

// answer sets what both stand-ins reply for a role.
func (w *world) answer(role, js string) {
	w.t.Helper()
	if !json.Valid([]byte(js)) {
		w.t.Fatalf("bad answer %s", js)
	}
	claude := fmt.Sprintf(`{"type":"result","result":"","structured_output":%s}`, js)
	text, _ := json.Marshal(js)
	codex := fmt.Sprintf(`{"type":"item.completed","item":{"type":"agent_message","text":%s}}`, text)
	os.WriteFile(filepath.Join(w.fakes, role+".claude"), []byte(claude), 0o644)
	os.WriteFile(filepath.Join(w.fakes, role+".codex"), []byte(codex), 0o644)
}

// flag creates or removes a switch file such as "codex.fail" or "select.sleep".
func (w *world) flag(name, content string, on bool) {
	path := filepath.Join(w.fakes, name)
	if on {
		os.WriteFile(path, []byte(content), 0o644)
	} else {
		os.Remove(path)
	}
}

func (w *world) calls() []string {
	b, _ := os.ReadFile(filepath.Join(w.fakes, "calls"))
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "_"))
}

// spin runs the binary in dir with stdin and returns stdout; it fails the
// test on a non-zero exit.
func (w *world) spin(dir, stdin string, args ...string) string {
	w.t.Helper()
	out, errOut, code := w.run(dir, stdin, nil, args...)
	if code != 0 {
		w.t.Fatalf("spin %v: exit %d\n%s%s", args, code, out, errOut)
	}
	return out
}

func (w *world) run(dir, stdin string, extraEnv []string, args ...string) (stdout, stderr string, code int) {
	w.t.Helper()
	cmd := exec.Command(spinBin, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, append(append([]string{}, w.env...), extraEnv...), strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

// hook sends one event as the agent would and returns the context it added.
func (w *world) hook(agent string, ev map[string]any, extraEnv ...string) string {
	w.t.Helper()
	if ev["cwd"] == nil {
		ev["cwd"] = w.work
	}
	b, _ := json.Marshal(ev)
	cmd := exec.Command(spinBin, "hook", "--agent", agent)
	cmd.Dir, cmd.Env, cmd.Stdin = w.work, append(append([]string{}, w.env...), extraEnv...), bytes.NewReader(b)
	out, err := cmd.Output()
	if err != nil {
		w.t.Fatalf("hook %v: %v", ev["hook_event_name"], err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return ""
	}
	var o struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &o); err != nil || o.HookSpecificOutput.HookEventName != ev["hook_event_name"] {
		w.t.Fatalf("hook output %q: %v", out, err)
	}
	return o.HookSpecificOutput.AdditionalContext
}

// waitFor polls until ok or fails after 15 s; background workers are detached.
func (w *world) waitFor(what string, ok func() bool) {
	w.t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	log, _ := os.ReadFile(filepath.Join(w.home, "worker.log"))
	w.t.Fatalf("timed out waiting for %s; calls %v\nworker.log:\n%s", what, w.calls(), log)
}

// idle waits until the queue is empty and no worker holds its lock.
func (w *world) idle() {
	w.t.Helper()
	w.waitFor("the worker to finish", func() bool {
		jobs, _ := filepath.Glob(filepath.Join(w.home, "queue", "*.json"))
		if len(jobs) > 0 {
			return false
		}
		f, err := os.OpenFile(filepath.Join(w.home, "worker.lock"), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return false
		}
		defer f.Close()
		return tryLock(f)
	})
}

func (w *world) memory(store, name string) string {
	dir := filepath.Join(w.home, "projects", store)
	if store == "global" {
		dir = filepath.Join(w.home, "global")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "memory", name+".md"))
	return string(b)
}

func claudeTranscript(t *testing.T, path, session, cwd string, msgs ...string) {
	t.Helper()
	var b strings.Builder
	start := time.Now().Add(-time.Hour)
	for i, m := range msgs {
		role := []string{"user", "assistant"}[i%2]
		line, _ := json.Marshal(map[string]any{"type": role, "sessionId": session, "cwd": cwd,
			"timestamp": start.Add(time.Duration(i) * time.Minute), "message": map[string]any{"content": m}})
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func codexTranscript(t *testing.T, path, session, cwd string, msgs ...string) {
	t.Helper()
	start := time.Now().Add(-time.Hour)
	meta, _ := json.Marshal(map[string]any{"timestamp": start, "type": "session_meta", "payload": map[string]any{"id": session, "cwd": cwd, "source": "cli"}})
	lines := []string{string(meta)}
	for i, m := range msgs {
		role, kind := "user", "input_text"
		if i%2 == 1 {
			role, kind = "assistant", "output_text"
		}
		line, _ := json.Marshal(map[string]any{"timestamp": start.Add(time.Duration(i+1) * time.Minute), "type": "response_item",
			"payload": map[string]any{"type": "message", "role": role, "content": []map[string]any{{"type": kind, "text": m}}}})
		lines = append(lines, string(line))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const deployOps = `{"ops":[{"op":"write","name":"deploy-pipeline","type":"reference","description":"Deploys run in the Buildkite pipeline ship-it","body":"Buildkite ship-it.","reason":""}]}`

// One Claude Code session from start to recall: the index at start, a turn
// that extracts in the background, and the memory recalled in a later
// session, both in time and, when the selector is slow, after a tool call.
func TestClaudeSessionEndToEnd(t *testing.T) {
	w := newWorld(t)
	w.answer("read", `{"nothing_to_save":false,"read":[]}`)
	w.answer("write", deployOps)
	w.answer("select", `{"selected_memories":["proj/deploy-pipeline.md"]}`)

	start := w.hook("claude", map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "source": "startup"})
	if !strings.Contains(start, "memory_write") || !strings.Contains(start, "# proj (this folder's project)\n\nNo memories yet.") {
		t.Fatalf("session start:\n%s", start)
	}
	tp := filepath.Join(t.TempDir(), "s1.jsonl")
	claudeTranscript(t, tp, "s1", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	began := time.Now()
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s1", "transcript_path": tp})
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("Stop hook took %v; extraction must run detached", d)
	}
	w.waitFor("the extracted memory", func() bool { return w.memory("proj", "deploy-pipeline") != "" })
	w.idle()
	if got := strings.Join(w.calls(), ","); got != "codex_read,codex_write" {
		t.Errorf("extraction calls %s, want Codex first", got)
	}

	if start := w.hook("claude", map[string]any{"hook_event_name": "SessionStart", "session_id": "s2", "source": "startup"}); !strings.Contains(start, "[deploy-pipeline](proj/deploy-pipeline.md)") {
		t.Fatalf("next session's index:\n%s", start)
	}
	got := w.hook("claude", map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s2", "prompt": "how do we deploy this?", "transcript_path": tp})
	if !strings.Contains(got, "Memory: proj/deploy-pipeline.md") || !strings.Contains(got, "Buildkite ship-it.") {
		t.Fatalf("recall with the prompt:\n%s", got)
	}
	if c := w.calls(); c[len(c)-1] != "claude_select" {
		t.Errorf("selector ran on %s, want the session's own CLI", c[len(c)-1])
	}

	// A selector slower than the wait: nothing with the prompt, then the
	// picks ride the next tool call.
	w.flag("select.sleep", "1.5", true)
	began = time.Now()
	got = w.hook("claude", map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s3", "prompt": "how do we deploy this?"}, "SPINDLE_RECALL_WAIT=0.3")
	if got != "" || time.Since(began) > 1200*time.Millisecond {
		t.Fatalf("slow selector: %q after %v", got, time.Since(began))
	}
	var late string
	w.waitFor("late picks", func() bool {
		late = w.hook("claude", map[string]any{"hook_event_name": "PostToolUse", "session_id": "s3"})
		return late != ""
	})
	if !strings.Contains(late, "Memory: proj/deploy-pipeline.md") {
		t.Fatalf("after a tool call:\n%s", late)
	}
	if again := w.hook("claude", map[string]any{"hook_event_name": "PostToolUse", "session_id": "s3"}); again != "" {
		t.Errorf("late picks attached twice")
	}
	evs := w.spin(w.work, "", "log", "--kind", "recall", "--json")
	if !strings.Contains(evs, `"note":"with the prompt"`) || !strings.Contains(evs, `"note":"after a tool call"`) {
		t.Errorf("activity log lacks the delivery split:\n%s", evs)
	}
}

// What a Codex session saves, a Claude Code session in the same folder recalls.
func TestCodexSavesClaudeRecalls(t *testing.T) {
	w := newWorld(t)
	w.answer("read", `{"nothing_to_save":false,"read":[]}`)
	w.answer("write", `{"ops":[{"op":"write","name":"no-mocks","type":"directive","description":"Integration tests hit a real database","body":"Real DB.\n\n**Why:** mocks hid a broken migration.\n**How to apply:** storage tests.","reason":""}]}`)
	w.answer("select", `{"selected_memories":["no-mocks.md"]}`)
	tp := filepath.Join(t.TempDir(), "rollout.jsonl")
	codexTranscript(t, tp, "019e-codex", w.work, "Don't mock the database in integration tests, we got burned by a broken migration.", "Understood.")
	w.hook("codex", map[string]any{"hook_event_name": "Stop", "session_id": "019e-codex", "transcript_path": tp})
	w.waitFor("the directive", func() bool { return w.memory("proj", "no-mocks") != "" })
	w.idle()
	got := w.hook("claude", map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "c1", "prompt": "should I mock the database in this test?"})
	if !strings.Contains(got, "Memory: proj/no-mocks.md") || !strings.Contains(got, "**Why:** mocks hid") {
		t.Fatalf("recall in Claude Code:\n%s", got)
	}
	if cur, _ := os.ReadFile(filepath.Join(w.home, "cursors.json")); !strings.Contains(string(cur), `"agent": "codex"`) {
		t.Errorf("cursor not recorded as codex:\n%s", cur)
	}
}

// Codex broken: Claude answers. Both broken: nothing is lost, and the next
// turn retries the same messages.
func TestFallbackAndRetry(t *testing.T) {
	w := newWorld(t)
	w.answer("read", `{"nothing_to_save":false,"read":[]}`)
	w.answer("write", deployOps)
	w.flag("codex.fail", "", true)
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the memory via Claude", func() bool { return w.memory("proj", "deploy-pipeline") != "" })
	w.idle()
	if got := strings.Join(w.calls(), ","); got != "codex_read,claude_read,codex_write,claude_write" {
		t.Errorf("calls %s", got)
	}

	w.flag("claude.fail", "", true)
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.",
		"Staging is shared with QA, so never reset it without asking.", "Got it.")
	w.answer("write", `{"ops":[{"op":"write","name":"staging-shared","type":"project","description":"Staging is shared with QA","body":"Shared.","reason":""}]}`)
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the failed pass", func() bool { return len(w.calls()) >= 6 })
	w.idle()
	if cur, _ := os.ReadFile(filepath.Join(w.home, "cursors.json")); !strings.Contains(string(cur), `"line": 2`) {
		t.Fatalf("cursor moved past a failed pass:\n%s", cur)
	}

	w.flag("codex.fail", "", false)
	w.flag("claude.fail", "", false)
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the retried memory", func() bool { return w.memory("proj", "staging-shared") != "" })
	w.idle()
}

// Ten Stops at once for one transcript: one worker, one pass.
func TestManyStopsOnePass(t *testing.T) {
	w := newWorld(t)
	w.answer("read", `{"nothing_to_save":false,"read":[]}`)
	w.answer("write", deployOps)
	w.flag("read.sleep", "0.3", true)
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
		}()
	}
	wg.Wait()
	w.waitFor("the memory", func() bool { return w.memory("proj", "deploy-pipeline") != "" })
	w.idle()
	time.Sleep(300 * time.Millisecond)
	w.idle()
	if got := strings.Join(w.calls(), ","); got != "codex_read,codex_write" {
		t.Errorf("calls %s, want one pass", got)
	}
	if ch, _ := os.ReadFile(filepath.Join(w.home, "projects", "proj", "changes.jsonl")); bytes.Count(ch, []byte("\n")) != 1 {
		t.Errorf("changes.jsonl:\n%s", ch)
	}
}

// Invariant 17: hooks fired inside spindle's own model calls do nothing.
func TestNoRecursion(t *testing.T) {
	w := newWorld(t)
	w.answer("read", `{"nothing_to_save":true,"read":[]}`)
	w.flag("recurse", "", true)
	claudeTranscript(t, filepath.Join(w.fakes, "inner.jsonl"), "inner", w.work, "This transcript must never be extracted, it belongs to a side call.", "OK.")
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the pass", func() bool { return len(w.calls()) >= 1 })
	w.idle()
	time.Sleep(300 * time.Millisecond)
	w.idle()
	if got := strings.Join(w.calls(), ","); got != "codex_read" {
		t.Errorf("calls %s: a side call's hook started more work", got)
	}
	if evs, _ := os.ReadFile(filepath.Join(w.home, "events.jsonl")); strings.Contains(string(evs), `"session":"inner"`) {
		t.Errorf("activity from inside a side call:\n%s", evs)
	}
}

// Invariant 16: whatever happens, the hook exits 0 and prints nothing bad.
func TestHookAlwaysExitsZero(t *testing.T) {
	w := newWorld(t)
	for name, c := range map[string]struct {
		stdin string
		env   []string
	}{
		"garbage stdin":         {stdin: "not json"},
		"empty stdin":           {stdin: ""},
		"relative SPINDLE_HOME": {stdin: `{"hook_event_name":"SessionStart","session_id":"s"}`, env: []string{"SPINDLE_HOME=relative/path"}},
		"missing transcript":    {stdin: `{"hook_event_name":"Stop","session_id":"s","transcript_path":"/nope.jsonl"}`},
	} {
		out, errOut, code := w.run(w.work, c.stdin, c.env, "hook", "--agent", "claude")
		if code != 0 {
			t.Errorf("%s: exit %d: %s", name, code, errOut)
		}
		if strings.TrimSpace(out) != "" && !json.Valid([]byte(out)) {
			t.Errorf("%s: output is not JSON: %q", name, out)
		}
	}
	if log, _ := os.ReadFile(filepath.Join(w.home, "hooks.log")); !strings.Contains(string(log), "hook input") {
		t.Errorf("bad input not logged:\n%s", log)
	}
	w.idle() // the Stop's worker writes to the home until it exits
}

// Invariant 15: hooks are fast even with a big memory and a hung selector.
func TestHookSpeed(t *testing.T) {
	w := newWorld(t)
	mem := filepath.Join(w.home, "projects", "proj", "memory")
	for i := 0; i < 1000; i++ {
		text := fmt.Sprintf("---\nname: m-%04d\ndescription: memory number %d with a realistic one-line description\nmetadata:\n  type: project\n---\n\nBody %d.\n", i, i, i)
		os.WriteFile(filepath.Join(mem, fmt.Sprintf("m-%04d.md", i)), []byte(text), 0o644)
	}
	w.spin(w.work, "Rebuild.", "write", "--type", "project", "--description", "trigger an index rebuild", "rebuild")
	began := time.Now()
	if s := w.hook("claude", map[string]any{"hook_event_name": "SessionStart", "session_id": "s", "source": "startup"}); len(s) == 0 {
		t.Fatal("no start context")
	}
	if d := time.Since(began); d > time.Second {
		t.Errorf("session start took %v with 1,000 memories", d)
	}
	w.answer("select", `{"selected_memories":[]}`)
	w.flag("select.sleep", "10", true)
	began = time.Now()
	w.hook("claude", map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "tell me about memory number 7"}, "SPINDLE_RECALL_WAIT=0.5")
	if d := time.Since(began); d > 1500*time.Millisecond {
		t.Errorf("prompt hook took %v with a 0.5 s wait", d)
	}
}

// Invariant 21: the CLI and the MCP tools are one core, with the same text.
func TestCLIAndMCPSayTheSame(t *testing.T) {
	cli, srv := newWorld(t), newWorld(t)
	type step struct {
		cli   []string
		stdin string
		tool  string
		args  map[string]any
	}
	body := "Real DB.\n\n**Why:** mocks hid a broken migration.\n**How to apply:** storage tests."
	steps := []step{
		{[]string{"write", "--type", "directive", "--description", "Integration tests hit a real database", "no-mocks"}, body, "memory_write", map[string]any{"name": "no-mocks", "type": "directive", "description": "Integration tests hit a real database", "body": body}},
		{[]string{"write", "--global", "--type", "user", "--description", "Solo dev", "role"}, "Go.", "memory_write", map[string]any{"name": "role", "type": "user", "project": "global", "description": "Solo dev", "body": "Go."}},
		{[]string{"show", "no-mocks"}, "", "memory_read", map[string]any{"name": "no-mocks"}},
		{[]string{"search", "database", "tests"}, "", "memory_search", map[string]any{"query": "database tests"}},
		{[]string{"index"}, "", "memory_index", nil},
		{[]string{"projects"}, "", "memory_projects", nil},
		{[]string{"history", "no-mocks"}, "", "memory_history", map[string]any{"name": "no-mocks"}},
		{[]string{"rm", "--reason", "test", "no-mocks"}, "", "memory_delete", map[string]any{"name": "no-mocks", "reason": "test"}},
	}
	var rpc []string
	rpc = append(rpc, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	for i, s := range steps {
		args := s.args
		if args == nil {
			args = map[string]any{}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call", "params": map[string]any{"name": s.tool, "arguments": args}})
		rpc = append(rpc, string(b))
	}
	out := srv.spin(srv.work, strings.Join(rpc, "\n")+"\n", "mcp", "--agent", "claude")
	results := map[int]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r struct {
			ID     int
			Result struct{ Content []struct{ Text string } }
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && len(r.Result.Content) > 0 {
			results[r.ID] = r.Result.Content[0].Text
		}
	}
	norm := func(s, home, work string) string {
		return strings.TrimSpace(strings.NewReplacer(home, "HOME", work, "WORK").Replace(s))
	}
	for i, s := range steps {
		c := norm(cli.spin(cli.work, s.stdin, s.cli...), cli.home, cli.work)
		m := norm(results[i+1], srv.home, srv.work)
		if s.tool == "memory_history" { // who made the change differs by design: cli vs agent
			c = strings.ReplaceAll(c, "by cli", "by WHO")
			m = strings.ReplaceAll(m, "by agent", "by WHO")
		}
		if c != m {
			t.Errorf("%s vs spin %s:\nCLI:\n%s\nMCP:\n%s", s.tool, s.cli[0], c, m)
		}
	}
}

func tryLock(f *os.File) bool {
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

// A session no hook reported, such as one whose hooks weren't installed, is
// found by the sweep the hourly dream check runs, extracted, and counted.
func TestSweepFindsHooklessSession(t *testing.T) {
	w := newWorld(t)
	w.answer("read", `{"nothing_to_save":false,"read":[]}`)
	w.answer("write", deployOps)
	home := ""
	for _, e := range w.env {
		if v, ok := strings.CutPrefix(e, "HOME="); ok {
			home = v
		}
	}
	if out := w.spin(w.work, "", "dream", "--due"); !strings.Contains(out, "nothing due") {
		t.Fatalf("dream --due: %s", out)
	}
	if got := strings.Join(w.calls(), ","); got != "" {
		t.Fatalf("the first sweep read old sessions: %s", got)
	}
	// Move the marks back, as if the first sweep ran an hour ago, and leave
	// a settled transcript that changed since.
	marks := map[string]map[string]any{}
	b, _ := os.ReadFile(filepath.Join(w.home, "sweeps.json"))
	json.Unmarshal(b, &marks)
	for _, m := range marks {
		m["swept"] = time.Now().Add(-time.Hour).UTC()
	}
	b, _ = json.Marshal(marks)
	os.WriteFile(filepath.Join(w.home, "sweeps.json"), b, 0o644)
	dir := filepath.Join(home, ".claude", "projects", "-work")
	os.MkdirAll(filepath.Join(dir, "s9", "subagents"), 0o755)
	tp := filepath.Join(dir, "s9.jsonl")
	claudeTranscript(t, tp, "s9", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	sub := filepath.Join(dir, "s9", "subagents", "agent-1.jsonl")
	claudeTranscript(t, sub, "s9", w.work, "A subagent's long task description, which is not the user talking.", "Done.")
	old := time.Now().Add(-20 * time.Minute)
	os.Chtimes(tp, old, old)
	os.Chtimes(sub, old, old)

	w.spin(w.work, "", "dream", "--due")
	if w.memory("proj", "deploy-pipeline") == "" {
		t.Fatalf("the swept session wasn't extracted; calls %v", w.calls())
	}
	if got := strings.Join(w.calls(), ","); got != "codex_read,codex_write" {
		t.Errorf("calls %s, want one pass", got)
	}
	state, _ := os.ReadFile(filepath.Join(w.home, "projects", "proj", "state.json"))
	if !strings.Contains(string(state), tp) {
		t.Errorf("the session didn't count toward the dream:\n%s", state)
	}
	out := w.spin(w.work, "", "sources")
	if !strings.Contains(out, "claude-code ("+filepath.Join(home, ".claude", "projects")+")") || !strings.Contains(out, "1 sessions changed, 1 queued for extraction") {
		t.Errorf("spin sources:\n%s", out)
	}
	// Read once, it isn't queued again.
	w.spin(w.work, "", "dream", "--due")
	if n := len(w.calls()); n != 2 {
		t.Errorf("a read session was extracted again: %v", w.calls())
	}
}

// Sources are added and removed from the CLI and MCP alike.
func TestSourcesAddRemove(t *testing.T) {
	cli, srv := newWorld(t), newWorld(t)
	type step struct {
		cli  []string
		args map[string]any
	}
	steps := []step{
		{[]string{"sources", "add", "claude-code", "--dir", "WORK/claude"}, map[string]any{"action": "add", "kind": "claude-code", "dir": "WORK/claude"}},
		{[]string{"sources", "add", "codex", "--dir", "WORK/codex"}, map[string]any{"action": "add", "kind": "codex", "dir": "WORK/codex"}},
		{[]string{"sources", "rm", "codex"}, map[string]any{"action": "remove", "kind": "codex"}},
	}
	var rpc []string
	rpc = append(rpc, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	for i, s := range steps {
		args := map[string]any{}
		for k, v := range s.args {
			args[k] = strings.ReplaceAll(fmt.Sprint(v), "WORK", srv.work)
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call", "params": map[string]any{"name": "memory_source", "arguments": args}})
		rpc = append(rpc, string(b))
	}
	out := srv.spin(srv.work, strings.Join(rpc, "\n")+"\n", "mcp", "--agent", "claude")
	results := map[int]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var r struct {
			ID     int
			Result struct{ Content []struct{ Text string } }
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && len(r.Result.Content) > 0 {
			results[r.ID] = r.Result.Content[0].Text
		}
	}
	var homes []string
	for _, w := range []*world{cli, srv} {
		for _, e := range w.env {
			if v, ok := strings.CutPrefix(e, "HOME="); ok {
				homes = append(homes, v)
			}
		}
	}
	norm := func(s string, w *world, home string) string {
		return strings.TrimSpace(strings.NewReplacer(w.home, "SPINDLE", w.work, "WORK", home, "HOME").Replace(s))
	}
	for i, s := range steps {
		args := make([]string, len(s.cli))
		for j, a := range s.cli {
			args[j] = strings.ReplaceAll(a, "WORK", cli.work)
		}
		c := norm(cli.spin(cli.work, "", args...), cli, homes[0])
		m := norm(results[i+1], srv, homes[1])
		if c != m {
			t.Errorf("step %d:\nCLI:\n%s\nMCP:\n%s", i, c, m)
		}
	}
	got := norm(cli.spin(cli.work, "", "sources"), cli, homes[0])
	if !strings.Contains(got, "claude-code (WORK/claude)") || strings.Contains(got, "codex") {
		t.Errorf("after the steps:\n%s", got)
	}
	if _, errOut, code := cli.run(cli.work, "", nil, "sources", "rm", "codex"); code == 0 || !strings.Contains(errOut, `no source "codex"`) {
		t.Errorf("removing a missing source: exit %d %s", code, errOut)
	}
	if _, errOut, code := cli.run(cli.work, "", nil, "sources", "add", "nope"); code == 0 || !strings.Contains(errOut, "unknown source kind") {
		t.Errorf("adding an unknown kind: exit %d %s", code, errOut)
	}
}
