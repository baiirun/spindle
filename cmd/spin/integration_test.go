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
agent=""
for a in "$@"; do
  [ "$prev" = "--json-schema" ] && schema="$a"
  [ "$prev" = "--output-schema" ] && schema=$(/bin/cat "$a")
  case "$a" in workspace-write|--allowedTools) agent=1 ;; esac
  prev="$a"
done
if [ -n "$agent" ]; then
  # A dream session: run the test's script of spin commands as the agent would.
  /bin/cat > "$d/task"
  echo "$bin session" >> "$d/calls"
  if [ -f "$d/$bin.fail" ]; then echo "$bin: broken" >&2; exit 1; fi
  SESSION=$(/usr/bin/sed -n 's/.*--session \([^ ]*\) .*/\1/p' "$d/task" | /usr/bin/head -1) /bin/sh "$d/session.sh" >> "$d/session.out" 2>&1
  if [ "$bin" = claude ]; then echo '{"type":"result","result":"dreamt","is_error":false}'
  else echo '{"type":"item.completed","item":{"type":"agent_message","text":"dreamt"}}'; fi
  exit 0
fi
/bin/cat > /dev/null
case "$schema" in
  *'"gists"'*) role=ingest ;;
  *) role=rollup ;;
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

// flag creates or removes a switch file such as "codex.fail" or "ingest.sleep".
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

// entries reads a store's log.
func (w *world) entries(store string) []map[string]any {
	dir := filepath.Join(w.home, "projects", store)
	if store == "global" || store == "unfiled" {
		dir = filepath.Join(w.home, store)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "log.jsonl"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
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

const deployGists = `{"gists":[{"gist":"Deploys go through the Buildkite pipeline ship-it.","line":1,"scope":"project"}]}`

// answerAll sets the replies every pass gets unless a test changes one.
func (w *world) answerAll() {
	w.answer("ingest", deployGists)
	w.answer("rollup", `{"line":"Set up deploys through Buildkite ship-it."}`)
}

// One Claude Code session: the index at start, a turn ingested in the
// background, its session summarized once idle, and the next session
// starting with what was noted.
func TestSessionEndToEnd(t *testing.T) {
	w := newWorld(t)
	w.answerAll()
	start := w.hook("claude", map[string]any{"hook_event_name": "SessionStart", "session_id": "s", "source": "startup"})
	if !strings.Contains(start, "memory_append") || !strings.Contains(start, "None yet.") {
		t.Fatalf("first start context:\n%s", start)
	}
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the entry", func() bool { return len(w.entries("proj")) == 1 })
	w.idle()
	if got := strings.Join(w.calls(), ","); got != "codex_ingest,codex_rollup" {
		t.Errorf("calls %s, want one ingest and the idle session's rollup", got)
	}
	if rs, _ := filepath.Glob(filepath.Join(w.home, "projects", "proj", "rollups", "s", "*.json")); len(rs) != 1 {
		t.Errorf("session rollups %v", rs)
	}
	next := w.hook("claude", map[string]any{"hook_event_name": "SessionStart", "session_id": "s2", "source": "startup"})
	if !strings.Contains(next, "Buildkite pipeline ship-it") {
		t.Errorf("next start context lacks the entry:\n%s", next)
	}
}

// A Codex failure falls back to Claude; when both fail the cursor stays,
// and the next Stop retries the same messages.
func TestFallbackAndRetry(t *testing.T) {
	w := newWorld(t)
	w.answerAll()
	w.flag("codex.fail", "", true)
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the entry via Claude", func() bool { return len(w.entries("proj")) == 1 })
	w.idle()
	if got := w.calls(); len(got) < 2 || got[0] != "codex_ingest" || got[1] != "claude_ingest" {
		t.Errorf("calls %v", got)
	}

	w.flag("claude.fail", "", true)
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.",
		"Staging is shared with QA, so never reset it without asking.", "Got it.")
	w.answer("ingest", `{"gists":[{"gist":"Staging is shared with QA: never reset it without asking.","line":3,"scope":"project"}]}`)
	n := len(w.calls())
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the failed pass", func() bool { return len(w.calls()) >= n+2 })
	w.idle()
	if cur, _ := os.ReadFile(filepath.Join(w.home, "cursors.json")); !strings.Contains(string(cur), `"line": 2`) {
		t.Fatalf("cursor moved past a failed pass:\n%s", cur)
	}

	w.flag("codex.fail", "", false)
	w.flag("claude.fail", "", false)
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the retried entry", func() bool { return len(w.entries("proj")) == 2 })
	w.idle()
}

// Ten Stops at once for one transcript: one worker, one ingest.
func TestManyStopsOnePass(t *testing.T) {
	w := newWorld(t)
	w.answerAll()
	w.flag("ingest.sleep", "0.3", true)
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
		}()
	}
	wg.Wait()
	w.waitFor("the entry", func() bool { return len(w.entries("proj")) == 1 })
	w.idle()
	time.Sleep(300 * time.Millisecond)
	w.idle()
	ingests := 0
	for _, c := range w.calls() {
		if strings.HasSuffix(c, "_ingest") {
			ingests++
		}
	}
	if ingests != 1 || len(w.entries("proj")) != 1 {
		t.Errorf("calls %v, %d entries; want one ingest", w.calls(), len(w.entries("proj")))
	}
}

// Hooks fired inside spindle's own model calls do nothing.
func TestNoRecursion(t *testing.T) {
	w := newWorld(t)
	w.answerAll()
	w.answer("ingest", `{"gists":[]}`)
	w.flag("recurse", "", true)
	claudeTranscript(t, filepath.Join(w.fakes, "inner.jsonl"), "inner", w.work, "This transcript must never be ingested, it belongs to a side call.", "OK.")
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, tp, "s", w.work, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	w.hook("claude", map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": tp})
	w.waitFor("the pass", func() bool { return len(w.calls()) >= 1 })
	w.idle()
	time.Sleep(300 * time.Millisecond)
	w.idle()
	if got := strings.Join(w.calls(), ","); got != "codex_ingest" {
		t.Errorf("calls %s: a side call's hook started more work", got)
	}
	if evs, _ := os.ReadFile(filepath.Join(w.home, "activity.jsonl")); strings.Contains(string(evs), `"session":"inner"`) {
		t.Errorf("activity from inside a side call:\n%s", evs)
	}
}

// Whatever happens, the hook exits 0 and prints nothing bad.
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
		"damaged log":           {stdin: `{"hook_event_name":"SessionStart","session_id":"s"}`},
	} {
		if name == "damaged log" {
			os.WriteFile(filepath.Join(w.home, "projects", "proj", "log.jsonl"), []byte("not json\n"), 0o644)
		}
		out, errOut, code := w.run(w.work, c.stdin, c.env, "hook", "--agent", "claude")
		if code != 0 {
			t.Errorf("%s: exit %d: %s", name, code, errOut)
		}
		if strings.TrimSpace(out) != "" && !json.Valid([]byte(out)) {
			t.Errorf("%s: output is not JSON: %q", name, out)
		}
		if name == "damaged log" && !strings.Contains(out, "memory_append") {
			t.Errorf("a damaged log stopped the session's index: %q", out)
		}
	}
	if log, _ := os.ReadFile(filepath.Join(w.home, "hooks.log")); !strings.Contains(string(log), "hook input") {
		t.Errorf("bad input not logged:\n%s", log)
	}
	w.idle()
}

// Session start stays fast with a big log, and within its budget.
func TestHookSpeed(t *testing.T) {
	w := newWorld(t)
	var b strings.Builder
	start := time.Now().Add(-90 * 24 * time.Hour)
	for i := range 20_000 {
		line, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("e-%06d", i), "at": start.Add(time.Duration(i) * 6 * time.Minute),
			"session": fmt.Sprintf("s%04d", i/20), "source": map[string]any{"kind": "cli"}, "gist": fmt.Sprintf("entry number %d with a realistic one-line gist about the work", i)})
		b.Write(line)
		b.WriteByte('\n')
	}
	os.WriteFile(filepath.Join(w.home, "projects", "proj", "log.jsonl"), []byte(b.String()), 0o644)
	began := time.Now()
	s := w.hook("claude", map[string]any{"hook_event_name": "SessionStart", "session_id": "s", "source": "startup"})
	if d := time.Since(began); d > time.Second {
		t.Errorf("session start took %v with 20,000 entries", d)
	}
	if len(s) == 0 || len(s) > 12_000 {
		t.Errorf("start context is %d bytes", len(s))
	}
}

// A dream is an agent session that edits through spin dream write: a dry
// run records the edit and writes nothing, a real run writes and records it,
// and the commands refuse to work outside a running dream.
func TestDreamSession(t *testing.T) {
	w := newWorld(t)
	w.answerAll()
	w.spin(w.work, "", "log", "append", "--gist", "Byron wants PR titles to start with feat: or fix:.")
	id := w.entries("proj")[0]["id"].(string)
	script := fmt.Sprintf(`"$SPIN" dream write --session "$SESSION" rules/git-and-prs --title "Git and PRs" --evidence %s --reason stated <<'EOF'
- PR titles start with feat: or fix:.
EOF
"$SPIN" dream write --session "$SESSION" wiki/ghost --title Ghost --kind tool --evidence e-nope --reason invented --body x
`, id)
	os.WriteFile(filepath.Join(w.fakes, "session.sh"), []byte(script), 0o644)

	out := w.spin(w.work, "", "dream", "--dry-run")
	if !strings.Contains(out, "would write rules/git-and-prs: stated") || !strings.Contains(out, "rejected wiki/ghost") || !strings.Contains(out, "by codex") {
		b, _ := os.ReadFile(filepath.Join(w.fakes, "session.out"))
		t.Errorf("dry run output:\n%s\nsession:\n%s", out, b)
	}
	rule := filepath.Join(w.home, "projects", "proj", "rules", "git-and-prs.md")
	if _, err := os.Stat(rule); err == nil {
		t.Fatal("the dry run wrote the rule")
	}
	if task, _ := os.ReadFile(filepath.Join(w.fakes, "task")); !strings.Contains(string(task), id) {
		t.Errorf("the session's task lacks the note:\n%s", task)
	}

	w.spin(w.work, "", "dream", "--force")
	if b, _ := os.ReadFile(rule); !strings.Contains(string(b), "feat: or fix:") || !strings.Contains(string(b), id) {
		t.Errorf("rule after dream:\n%s", b)
	}
	if hist := w.spin(w.work, "", "rules", "history", "git-and-prs"); !strings.Contains(hist, "created by dream: stated") {
		t.Errorf("history:\n%s", hist)
	}
	if _, errOut, code := w.run(w.work, "x", nil, "dream", "write", "--session", filepath.Join(w.home, "projects", "proj", "dream-session.json"), "rules/late", "--title", "Late", "--evidence", id, "--reason", "r"); code == 0 {
		t.Errorf("dream write worked with no dream running: %s", errOut)
	}
}

// The CLI and the MCP tools are one core, with the same text.
func TestCLIAndMCPSayTheSame(t *testing.T) {
	w := newWorld(t)
	w.spin(w.work, "", "log", "append", "--gist", "Deploys go through the Buildkite pipeline ship-it.")
	cli := w.spin(w.work, "", "search", "buildkite")
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"buildkite"}}}
`
	out := w.spin(w.work, req, "mcp", "--agent", "claude")
	sc := bufio.NewScanner(strings.NewReader(out))
	var got string
	for sc.Scan() {
		var r struct {
			ID     int
			Result struct{ Content []struct{ Text string } }
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID == 2 {
			got = r.Result.Content[0].Text
		}
	}
	if got != cli {
		t.Errorf("MCP said:\n%s\nCLI said:\n%s", got, cli)
	}
}

func tryLock(f *os.File) bool {
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}
