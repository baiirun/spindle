package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindle/internal/model"
)

var t0 = time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)

func project(t *testing.T) Project {
	t.Helper()
	t.Setenv(ProjectEnv, "")
	home, root := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	p, err := Resolve(home, filepath.Join(root))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func write(t *testing.T, p Project, ops ...Op) Result {
	t.Helper()
	res, err := p.Apply(ops, "agent", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// claudeTranscript writes alternating user/assistant messages, a minute apart from start.
func claudeTranscript(t *testing.T, path, cwd string, start time.Time, msgs ...string) {
	t.Helper()
	var b strings.Builder
	for i, m := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		line, _ := json.Marshal(map[string]any{"type": role, "sessionId": "sess-" + filepath.Base(path), "cwd": cwd,
			"timestamp": start.Add(time.Duration(i) * time.Minute), "message": map[string]any{"content": m}})
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	t.Setenv(ProjectEnv, "")
	home, root := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	p, _ := Resolve(home, filepath.Join(root, "a", "b"))
	if p.Root != root || p != At(home, root) {
		t.Fatalf("project = %+v", p)
	}
	t.Setenv(ProjectEnv, "/x/y")
	if p, _ := Resolve(home, root); p.Root != "/x/y" {
		t.Fatalf("override ignored: %s", p.Root)
	}
}

func TestFileFormatRoundTrip(t *testing.T) {
	m := Memory{Name: "merge-freeze", Description: "Merge freeze from 2026-03-05", Type: "project", Body: "Freeze.\n\n**Why:** release.\n**How to apply:** flag PRs."}
	text := m.Render()
	if !strings.Contains(text, "metadata:\n  type: project\n") {
		t.Fatalf("not Claude Code's frontmatter:\n%s", text)
	}
	got, err := Parse(text)
	if err != nil || got.Name != m.Name || got.Description != m.Description || got.Type != m.Type || got.Body != m.Body {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if got, _ := Parse("---\nname: x\ndescription: d\ntype: user\n---\nbody"); got.Type != "user" {
		t.Fatalf("flat type key not read: %+v", got)
	}
}

func TestApplyOverwritesDeletesAndLogs(t *testing.T) {
	p := project(t)
	res := write(t, p,
		Op{Op: "write", Name: "Event Log Store", Type: "project", Description: "Event log in SQLite", Body: "SQLite."},
		Op{Op: "write", Name: "bad", Type: "feedback", Description: "old type name", Body: "x"},
		Op{Op: "write", Name: "nobody", Type: "user", Description: "d"},
		Op{Op: "delete", Name: "missing"},
		Op{Op: "write", Name: "MEMORY", Type: "user", Description: "d", Body: "b"},
	)
	if res.Written != 1 || res.Skipped != 4 {
		t.Fatalf("apply = %+v", res)
	}
	write(t, p, Op{Op: "write", Name: "event-log-store", Body: "Postgres, for concurrent writers."})
	m, ok := p.Get("event-log-store")
	if !ok || m.Body != "Postgres, for concurrent writers." || m.Type != "project" || m.Description != "Event log in SQLite" {
		t.Fatalf("overwrite kept the wrong fields: %+v", m)
	}
	if strings.Contains(m.Render(), "SQLite.\n") {
		t.Fatal("old body kept in the file")
	}
	if !strings.Contains(p.ReadIndex(), "- [event-log-store](event-log-store.md) — Event log in SQLite") {
		t.Fatalf("index:\n%s", p.ReadIndex())
	}
	write(t, p, Op{Op: "delete", Name: "event-log-store", Reason: "wrong"})
	if _, ok := p.Get("event-log-store"); ok {
		t.Fatal("not deleted")
	}
	changes, _ := p.Changes()
	if len(changes) != 3 || changes[1].Old == "" || changes[2].Op != "delete" || !strings.Contains(changes[2].Old, "Postgres") {
		t.Fatalf("audit log = %+v", changes)
	}
	if strings.Contains(p.ReadIndex(), "event-log-store") {
		t.Fatal("index still lists a deleted memory")
	}
}

func TestIndexLimits(t *testing.T) {
	var mems []Memory
	for i := 0; i < 300; i++ {
		mems = append(mems, Memory{Name: fmt.Sprintf("p-%03d", i), Type: "project", Description: "a decision"})
	}
	mems = append(mems, Memory{Name: "role", Type: "user", Description: "backend dev"})
	idx := Index(mems)
	if n := strings.Count(idx, "\n"); n > IndexLines {
		t.Fatalf("index has %d lines", n)
	}
	if !strings.Contains(idx, "[role]") || !strings.Contains(idx, "[p-000]") || !strings.Contains(idx, "older memories not listed") {
		t.Fatalf("index:\n%s", idx)
	}
	if strings.Contains(idx, "[p-299]") {
		t.Fatal("overflow kept the oldest instead of the newest")
	}
}

// fake answers the two extraction steps, the selector and the dream.
func fake(t *testing.T, calls *[]string, answer func(system, prompt string) string) model.Model {
	return model.Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		*calls = append(*calls, prompt)
		return answer(system, prompt), nil
	})
}

func TestExtract(t *testing.T) {
	p := project(t)
	write(t, p, Op{Op: "write", Name: "db-choice", Type: "project", Description: "Database is SQLite", Body: "SQLite."})
	path := filepath.Join(t.TempDir(), "a.jsonl")
	claudeTranscript(t, path, p.Root, t0.Add(time.Hour), "Switch the database to Postgres, we need concurrent writers.", "Switching.")
	var calls []string
	m := fake(t, &calls, func(system, prompt string) string {
		if strings.Contains(system, "Step 1 of 2") {
			if !strings.Contains(prompt, "[project] db-choice.md") || !strings.Contains(prompt, "Switch the database") {
				t.Errorf("step 1 prompt:\n%s", prompt)
			}
			return `{"nothing_to_save":false,"read":["db-choice.md","nope.md"]}`
		}
		if !strings.Contains(prompt, `<file name="db-choice.md">`) {
			t.Errorf("step 2 did not get the file it asked for:\n%s", prompt)
		}
		return `{"ops":[{"op":"write","name":"db-choice","type":"project","description":"Database is Postgres (since 2026-10-25)","body":"Postgres.\n\n**Why:** concurrent writers.\n**How to apply:** no SQLite.","reason":""}]}`
	})
	res, err := Extract(context.Background(), p, path, m, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Skip != "" || res.Written != 1 || len(calls) != 2 {
		t.Fatalf("extract = %+v, %d calls", res, len(calls))
	}
	if mem, _ := p.Get("db-choice"); !strings.HasPrefix(mem.Body, "Postgres.") {
		t.Fatalf("memory = %+v", mem)
	}
	st, _ := p.LoadState()
	if st.Cursors[path].Line != 2 || st.Sessions["sess-a.jsonl"] != path {
		t.Fatalf("state = %+v", st)
	}
	// Nothing new: no call.
	if res, _ := Extract(context.Background(), p, path, m, t0.Add(3*time.Hour)); res.Skip != "no new messages" || len(calls) != 2 {
		t.Fatalf("re-ran on an unchanged transcript: %+v", res)
	}
	// A bare "ok": skipped, cursor still moves.
	claudeTranscript(t, path, p.Root, t0.Add(time.Hour), "Switch the database to Postgres, we need concurrent writers.", "Switching.", "ok", "Done.")
	if res, _ := Extract(context.Background(), p, path, m, t0.Add(3*time.Hour)); res.Skip != "too little user text" || len(calls) != 2 {
		t.Fatalf("short turn = %+v", res)
	}
	if st, _ := p.LoadState(); st.Cursors[path].Line != 4 {
		t.Fatalf("cursor did not move past a skipped turn")
	}
	// The main agent wrote memory during these messages: skipped.
	claudeTranscript(t, path, p.Root, t0.Add(time.Hour), "Switch the database to Postgres, we need concurrent writers.", "Switching.", "ok", "Done.", "Remember that the staging DB is shared with QA.", "Saved.")
	write(t, p, Op{Op: "write", Name: "staging-db", Type: "project", Description: "Staging DB shared with QA", Body: "Shared."})
	// write() stamps t0; the new messages are after t0+1h, so make the agent write later.
	p.Apply([]Op{{Op: "write", Name: "staging-db", Type: "project", Description: "Staging DB shared with QA", Body: "Shared with QA."}}, "agent", "", t0.Add(90*time.Minute))
	if res, _ := Extract(context.Background(), p, path, m, t0.Add(4*time.Hour)); res.Skip != "agent wrote memory" || len(calls) != 2 {
		t.Fatalf("agent-written turn = %+v", res)
	}
}

func TestExtractNothingToSave(t *testing.T) {
	p := project(t)
	path := filepath.Join(t.TempDir(), "a.jsonl")
	claudeTranscript(t, path, p.Root, t0, "What does this function do exactly?", "It parses.")
	var calls []string
	m := fake(t, &calls, func(string, string) string { return `{"nothing_to_save":true,"read":[]}` })
	if res, err := Extract(context.Background(), p, path, m, t0); err != nil || res.Written != 0 || len(calls) != 1 {
		t.Fatalf("nothing to save = %+v, %v, %d calls", res, err, len(calls))
	}
}

func TestRecall(t *testing.T) {
	p := project(t)
	if id, _ := p.StartRecall("s", "", "where are events stored?", t0); id != "" {
		t.Fatal("recall started with no memories")
	}
	write(t, p,
		Op{Op: "write", Name: "event-log-store", Type: "project", Description: "Event log lives in Postgres", Body: "Postgres."},
		Op{Op: "write", Name: "user-role", Type: "user", Description: "Backend developer", Body: "Go."},
	)
	if id, _ := p.StartRecall("s", "", "thanks", t0); id != "" {
		t.Fatal("one-word prompt triggered recall")
	}
	id, err := p.StartRecall("s", "", "where are events stored?", t0)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	if text, _ := p.WaitRecall("s", id, 50*time.Millisecond); text != "" {
		t.Fatal("attached before the selector finished")
	}
	var calls []string
	m := fake(t, &calls, func(system, prompt string) string {
		if !strings.Contains(system, "You are selecting memories") || !strings.Contains(prompt, "[project] event-log-store.md (") || !strings.HasSuffix(prompt, "Select memories relevant to:\nwhere are events stored?") {
			t.Errorf("selector prompt:\n%s", prompt)
		}
		return `{"selected_memories":["event-log-store.md","made-up.md"]}`
	})
	if err := p.RunSelect(context.Background(), "s", id, m, false); err != nil {
		t.Fatal(err)
	}
	text, err := p.AttachPending("s")
	if err != nil || !strings.HasPrefix(text, RecallPrefix) || !strings.Contains(text, "Memory: event-log-store.md:") || strings.Contains(text, "made-up") {
		t.Fatalf("attachment = %q, %v", text, err)
	}
	if again, _ := p.AttachPending("s"); again != "" {
		t.Fatal("attached twice")
	}
	// The same file is not shown twice in a session, and the selector is told.
	id, _ = p.StartRecall("s", "", "events again please", t0)
	m = fake(t, &calls, func(system, prompt string) string {
		if !strings.Contains(prompt, "Already returned for earlier queries in this conversation: event-log-store.md") {
			t.Errorf("selector not told what was shown:\n%s", prompt)
		}
		return `{"selected_memories":["event-log-store.md"]}`
	})
	p.RunSelect(context.Background(), "s", id, m, false)
	if text, _ := p.WaitRecall("s", id, time.Second); text != "" {
		t.Fatalf("re-attached a surfaced memory: %q", text)
	}
	// After compaction it may be shown again.
	p.ResetRecall("s")
	id, _ = p.StartRecall("s", "", "where are events stored?", t0)
	p.RunSelect(context.Background(), "s", id, nil, true)
	if text, _ := p.WaitRecall("s", id, time.Second); !strings.Contains(text, "event-log-store.md") {
		t.Fatalf("local select after reset = %q", text)
	}
	// A turn that ends first drops the pick.
	id, _ = p.StartRecall("s2", "", "where are events stored?", t0)
	p.DropPending("s2")
	p.RunSelect(context.Background(), "s2", id, nil, true)
	if text, _ := p.AttachPending("s2"); text != "" {
		t.Fatalf("dropped pick attached: %q", text)
	}
}

func TestRecallAgeNoteAndBudget(t *testing.T) {
	m := Memory{Name: "x", Type: "project", Description: "d", Body: strings.Repeat("a", 5000), Modified: t0}
	text := recalled(m, t0.Add(72*time.Hour))
	if !strings.HasPrefix(text, "This memory is 3 days old.") || len(text) > FileBytes+400 {
		t.Fatalf("recalled = %d chars: %.120q", len(text), text)
	}
	p := project(t)
	write(t, p, Op{Op: "write", Name: "a", Type: "project", Description: "d", Body: "b"})
	p.withRecall("s", func(st *RecallState) error { st.Bytes = SessionBytes; return nil })
	if id, _ := p.StartRecall("s", "", "anything at all", t0); id != "" {
		t.Fatal("recall ran past the session budget")
	}
}

func TestDream(t *testing.T) {
	p := project(t)
	st := State{Sessions: map[string]string{"a": "", "b": "", "c": "", "d": ""}}
	if DreamDue(st, t0) {
		t.Fatal("due after 4 sessions")
	}
	st.Sessions["e"] = ""
	if !DreamDue(st, t0) {
		t.Fatal("first dream not due after 5 sessions")
	}
	st.LastDream = t0.Add(-23 * time.Hour)
	if DreamDue(st, t0) {
		t.Fatal("due within 24 h")
	}

	write(t, p,
		Op{Op: "write", Name: "terse-replies", Type: "directive", Description: "Keep replies short", Body: "Short."},
		Op{Op: "write", Name: "short-answers", Type: "directive", Description: "Short answers", Body: "Short."},
	)
	os.WriteFile(filepath.Join(p.Root, "CLAUDE.md"), []byte("Speak concisely."), 0o644)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	claudeTranscript(t, path, p.Root, t0, "From now on, no trailing summaries.", "OK.")
	state, _ := p.LoadState()
	state.Sessions["sess-s.jsonl"] = path
	state.LastDream = t0.Add(-48 * time.Hour)
	p.SaveState(state)
	var calls []string
	m := fake(t, &calls, func(system, prompt string) string {
		for _, want := range []string{`<file name="terse-replies.md"`, "Speak concisely.", "no trailing summaries"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("dream prompt lacks %q", want)
			}
		}
		return `{"ops":[{"op":"write","name":"terse-replies","type":"directive","description":"Short replies, no trailing summaries","body":"Short, no summaries.","reason":""},{"op":"delete","name":"short-answers","type":"","description":"","body":"","reason":"duplicate"}],"summary":"merged two"}`
	})
	res, err := Dream(context.Background(), p, m, t0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || res.Deleted != 1 || res.Summary != "merged two" {
		t.Fatalf("dream = %+v", res)
	}
	if state, _ = p.LoadState(); !state.LastDream.Equal(t0) || len(state.Sessions) != 0 {
		t.Fatalf("gate not reset: %+v", state)
	}
}

func TestWorker(t *testing.T) {
	p := project(t)
	path := filepath.Join(t.TempDir(), "a.jsonl")
	claudeTranscript(t, path, p.Root, t0, "Our deploys go through the Buildkite pipeline called ship-it.", "Noted.")
	var calls []string
	m := fake(t, &calls, func(system, prompt string) string {
		if strings.Contains(system, "Step 1 of 2") {
			return `{"nothing_to_save":false,"read":[]}`
		}
		return `{"ops":[{"op":"write","name":"deploy-pipeline","type":"reference","description":"Deploys run in Buildkite pipeline ship-it","body":"Buildkite ship-it.","reason":""}]}`
	})
	o := WorkerOptions{Models: func(string, model.Role) model.Model { return m }, Now: func() time.Time { return t0 }, NoDream: true}
	for i := 0; i < 3; i++ {
		Enqueue(p.Home, Job{Agent: "claude", Transcript: path, Root: p.Root})
	}
	if err := Work(context.Background(), p.Home, o); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(pending(p.Home)) != 0 {
		t.Fatalf("%d model calls (want one pass for three Stops), %d jobs left", len(calls), len(pending(p.Home)))
	}
	if _, ok := p.Get("deploy-pipeline"); !ok {
		t.Fatal("memory not written")
	}
	unlock, _ := lockFile(filepath.Join(p.Home, "worker.lock"), false)
	Enqueue(p.Home, Job{Agent: "claude", Transcript: path, Root: p.Root})
	Work(context.Background(), p.Home, o)
	if len(pending(p.Home)) != 1 {
		t.Fatal("a second worker ran while the first held the lock")
	}
	unlock()
}

func TestStartContext(t *testing.T) {
	p := project(t)
	if s := p.StartContext(); !strings.Contains(s, "directive:") || !strings.Contains(s, "(empty") {
		t.Fatalf("empty start context:\n%s", s)
	}
	write(t, p, Op{Op: "write", Name: "role", Type: "user", Description: "Backend dev", Body: "Go."})
	s := p.StartContext()
	if !strings.Contains(s, "- [role](role.md) — Backend dev") || len(s) > StartBudget {
		t.Fatalf("start context (%d chars):\n%s", len(s), s)
	}
}
