package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeModel answers extraction, Now and dream calls without a real model.
type fakeModel struct {
	mu      sync.Mutex
	prompts []string
	ops     func(system, prompt string) []Op
}

func (m *fakeModel) Text(ctx context.Context, system, prompt string) (string, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	m.mu.Unlock()
	return "## Where the work stands\nBuilding hooks.\n## Next step\nWrite tests.\n## Open questions and waiting on\nNone.", nil
}

func (m *fakeModel) JSON(ctx context.Context, system, prompt, schema string, out any) error {
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	m.mu.Unlock()
	var ops []Op
	if m.ops != nil {
		ops = m.ops(system, prompt)
	}
	b, _ := json.Marshal(map[string]any{"ops": ops})
	return json.Unmarshal(b, out)
}

var t0 = time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)

// writeClaude writes a Claude Code transcript of alternating user/assistant lines.
func writeClaude(t *testing.T, path, session, cwd string, msgs ...string) {
	t.Helper()
	var b strings.Builder
	for i, m := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		line, _ := json.Marshal(map[string]any{
			"type": role, "sessionId": session, "cwd": cwd, "timestamp": t0.Add(time.Duration(i) * time.Minute),
			"message": map[string]any{"role": role, "content": m},
		})
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCodex writes a Codex rollout with the same shape.
func writeCodex(t *testing.T, path, session, cwd string, msgs ...string) {
	t.Helper()
	var b strings.Builder
	meta, _ := json.Marshal(map[string]any{"timestamp": t0, "type": "session_meta", "payload": map[string]any{"id": session, "cwd": cwd}})
	b.Write(meta)
	b.WriteByte('\n')
	for i, m := range msgs {
		role, kind := "user", "input_text"
		if i%2 == 1 {
			role, kind = "assistant", "output_text"
		}
		line, _ := json.Marshal(map[string]any{
			"timestamp": t0.Add(time.Duration(i) * time.Minute), "type": "response_item",
			"payload": map[string]any{"type": "message", "role": role, "content": []map[string]any{{"type": kind, "text": m}}},
		})
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newProject(t *testing.T) (home string, p Project) {
	t.Helper()
	t.Setenv(ProjectEnv, "")
	home = t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := ResolveProject(home, root)
	if err != nil {
		t.Fatal(err)
	}
	return home, p
}

func TestResolveProject(t *testing.T) {
	t.Setenv(ProjectEnv, "")
	home, root := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	p, err := ResolveProject(home, sub)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root {
		t.Fatalf("root = %s, want the git top level %s", p.Root, root)
	}
	if !strings.HasPrefix(p.Slug, strings.ToLower(filepath.Base(root))+"-") || len(p.Slug) != len(filepath.Base(root))+9 {
		t.Fatalf("slug = %s", p.Slug)
	}
	if q := ProjectAt(home, root); q != p {
		t.Fatalf("ProjectAt = %#v, want %#v", q, p)
	}
	t.Setenv(ProjectEnv, "/elsewhere/proj")
	p, _ = ResolveProject(home, sub)
	if p.Root != "/elsewhere/proj" {
		t.Fatalf("override ignored: %s", p.Root)
	}
}

func TestDetectAgent(t *testing.T) {
	dir := t.TempDir()
	writeClaude(t, filepath.Join(dir, "c.jsonl"), "s", "/x", "hi")
	writeCodex(t, filepath.Join(dir, "x.jsonl"), "s", "/x", "hi")
	if a := DetectAgent(filepath.Join(dir, "c.jsonl")); a != "claude" {
		t.Fatalf("claude transcript detected as %s", a)
	}
	if a := DetectAgent(filepath.Join(dir, "x.jsonl")); a != "codex" {
		t.Fatalf("codex rollout detected as %s", a)
	}
}

func TestExtractBothFormats(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			home, p := newProject(t)
			path := filepath.Join(t.TempDir(), "s.jsonl")
			write := writeClaude
			if agent == "codex" {
				write = writeCodex
			}
			msgs := []string{"Use Postgres for the event log, not SQLite.", "Agreed, switching to Postgres.", "keep going", "Done with the schema."}
			write(t, path, "sess-0001-abcd", p.Root, msgs...)

			m := &fakeModel{ops: func(system, prompt string) []Op {
				if !strings.Contains(system, "decision record") {
					return nil
				}
				id := firstID(prompt)
				return []Op{{Op: "add", Kind: "decision", Key: "event log store", Text: "Postgres", Who: "user", Cites: []string{id, agent + ":deadbeef#L999"}}}
			}}
			corpusRoot := filepath.Join(home, "corpus")
			o := ExtractOptions{Model: m, Corpus: corpusRoot, Now: func() time.Time { return t0 }}

			res, err := Extract(context.Background(), p, path, "", false, o)
			if err != nil {
				t.Fatal(err)
			}
			if res.Folded || len(m.prompts) != 0 {
				t.Fatalf("folded below the threshold: %+v", res)
			}
			st, _ := p.LoadState()
			if c := st.Cursors[path]; c == nil || c.Pending == 0 || c.Line != 0 {
				t.Fatalf("cursor after a skipped pass = %+v", c)
			}

			res, err = Extract(context.Background(), p, path, "", true, o)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Folded || res.Users != 2 || res.Record.Added != 1 || res.Record.BadCites != 1 {
				t.Fatalf("forced pass = %+v", res)
			}
			rec, _ := p.LoadRecord()
			e := rec.Entries["event log store"]
			if e.Text != "Postgres" || len(e.Cites) != 1 || !strings.HasPrefix(e.Cites[0], agent+":sess-000#L") {
				t.Fatalf("entry = %+v", e)
			}
			now, _ := p.Now()
			if !strings.Contains(now, "keep going") || !strings.Contains(now, "Done with the schema.") || !strings.Contains(now, "## Next step") {
				t.Fatalf("now.md lacks the verbatim last exchange or the summary:\n%s", now)
			}
			users, _ := p.UserMessages()
			if len(users) != 2 || users[0].Text != msgs[0] || users[0].Agent != agent {
				t.Fatalf("user log = %+v", users)
			}
			if chunks, _ := filepath.Glob(filepath.Join(corpusRoot, agent, "*", "*.md")); len(chunks) != 1 {
				t.Fatalf("corpus chunks = %v", chunks)
			}
			st, _ = p.LoadState()
			if !st.SessionsSince["sess-0001-abcd"] || st.Cursors[path].Pending != 0 || st.Cursors[path].Line == 0 {
				t.Fatalf("state after fold = %+v / %+v", st, st.Cursors[path])
			}

			// Nothing new: no model call.
			n := len(m.prompts)
			if res, _ = Extract(context.Background(), p, path, "", true, o); res.Folded || len(m.prompts) != n {
				t.Fatalf("re-folded unchanged transcript")
			}
			// New turns: only they reach the record prompt.
			write(t, path, "sess-0001-abcd", p.Root, append(msgs, "Also add a recall cache.", "Added.")...)
			if res, _ = Extract(context.Background(), p, path, "", true, o); !res.Folded || res.Users != 1 {
				t.Fatalf("second fold = %+v", res)
			}
			last := m.prompts[n]
			if strings.Contains(last, "Use Postgres for the event log") || !strings.Contains(last, "recall cache") {
				t.Fatalf("record prompt re-sent old turns:\n%s", last)
			}
			if users, _ = p.UserMessages(); len(users) != 3 {
				t.Fatalf("user log has %d messages, want 3 (no duplicates)", len(users))
			}
		})
	}
}

func firstID(prompt string) string {
	i := strings.Index(prompt, "\n[")
	if i < 0 {
		return ""
	}
	rest := prompt[i+2:]
	return rest[:strings.Index(rest, " ")]
}

func TestRecordApply(t *testing.T) {
	var r Record
	at := t0
	r.Apply([]Op{{Op: "add", Kind: "decision", Key: "db", Text: "SQLite"}}, at, nil, "extract")
	res := r.Apply([]Op{
		{Op: "replace", Kind: "decision", Key: "db", Text: "Postgres", Why: "needs concurrent writers"},
		{Op: "add", Kind: "bogus", Key: "x", Text: "y"},
		{Op: "drop", Key: "missing", Reason: "?"},
		{Op: "add", Kind: "tried", Key: "", Text: "no key"},
	}, at.Add(time.Hour), nil, "extract")
	if res.Replaced != 1 || res.Skipped != 3 {
		t.Fatalf("apply = %+v", res)
	}
	e := r.Entries["db"]
	if e.Text != "Postgres" || !strings.Contains(e.Replaced, `"SQLite"`) {
		t.Fatalf("replace lost the old version: %+v", e)
	}
	r.Apply([]Op{{Op: "drop", Key: "db", Reason: "moved to a hosted store"}}, at.Add(2*time.Hour), nil, "tool")
	h := r.History("db")
	if len(r.Entries) != 0 || len(h) != 2 || h[0].Op != "replace" || h[1].Op != "drop" || h[1].Source != "tool" {
		t.Fatalf("history = %+v", h)
	}
}

func TestRecordSearchRelevance(t *testing.T) {
	r := Record{Entries: map[string]Entry{}}
	for i := 0; i < 10; i++ {
		r.Entries[fmt.Sprintf("hook %d", i)] = Entry{Kind: "decision", Text: "a hook rule"}
	}
	r.Entries["shadowdark damage dice"] = Entry{Kind: "decision", Text: "Use d6 for unarmed damage."}
	hits := r.Search("what damage do unarmed attacks do", 3)
	if len(hits) == 0 || hits[0].Key != "shadowdark damage dice" || !relevant(hits[0].Matched, hits[0].Rare) {
		t.Fatalf("hits = %+v", hits)
	}
	for _, h := range r.Search("the hook", 20) {
		if relevant(h.Matched, h.Rare) {
			t.Fatalf("one common term counted as relevant: %+v", h)
		}
	}
	if len(Terms("go keep going")) != 0 {
		t.Fatalf("a bare opener should have no search terms")
	}
}

func seed(t *testing.T, p Project) {
	t.Helper()
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	r := Record{Entries: map[string]Entry{}}
	r.Apply([]Op{
		{Op: "add", Kind: "decision", Key: "event log store", Text: "Postgres, not SQLite.", Who: "user"},
		{Op: "add", Kind: "preference", Key: "status updates", Text: "Post a short status every 10-15 minutes.", Who: "user"},
	}, t0, nil, "extract")
	p.SaveRecord(r)
	p.saveNow("_Written 2026-10-25 04:00Z from claude session old (transcript: /t/old.jsonl)._\n\n## Where the work stands\nHooks built.\n")
	p.appendUserMessages([]UserMessage{
		{ID: "claude:old00000#L1", Time: t0, Agent: "claude", Session: "old", Text: "Use Postgres for the event log."},
		{ID: "claude:cur00000#L1", Time: t0, Agent: "claude", Session: "cur", Text: "message from the current session"},
	})
}

func TestSessionStart(t *testing.T) {
	_, p := newProject(t)
	if text, _ := SessionStart(p, "startup", "cur", 0); text != "" {
		t.Fatalf("empty project injected %q", text)
	}
	seed(t, p)
	start, err := SessionStart(p, "startup", "cur", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hooks built.", "Use Postgres for the event log.", "event log store", "memory_search", "2 entries"} {
		if !strings.Contains(start, want) {
			t.Fatalf("startup context lacks %q:\n%s", want, start)
		}
	}
	if strings.Contains(start, "message from the current session") {
		t.Fatalf("startup context repeats the session's own messages")
	}
	compact, _ := SessionStart(p, "compact", "cur", 0)
	if strings.Contains(compact, "Use Postgres for the event log.") || !strings.Contains(compact, "Hooks built.") {
		t.Fatalf("compact context:\n%s", compact)
	}
	if small, _ := SessionStart(p, "startup", "cur", 400); len([]rune(small)) > 400+20 {
		t.Fatalf("budget ignored: %d chars", len(small))
	}
}

func TestRecall(t *testing.T) {
	_, p := newProject(t)
	seed(t, p)
	ctx := context.Background()
	text, err := Recall(ctx, p, "cur", "which store holds the event log?", 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "event log store") || !strings.Contains(text, "claude:old00000#L1") {
		t.Fatalf("recall = %q", text)
	}
	if strings.Contains(text, "status updates") {
		t.Fatalf("unrelated entry recalled: %q", text)
	}
	if again, _ := Recall(ctx, p, "cur", "which store holds the event log?", 0, time.Second); again != "" {
		t.Fatalf("same entries injected twice in one session: %q", again)
	}
	if other, _ := Recall(ctx, p, "other", "which store holds the event log?", 0, time.Second); other == "" {
		t.Fatalf("a new session should get the entries again")
	}
	if opener, _ := Recall(ctx, p, "x", "keep going", 0, time.Second); opener != "" {
		t.Fatalf("bare opener recalled %q", opener)
	}
}

func TestDreamGateAndPass(t *testing.T) {
	s := State{SessionsSince: map[string]bool{"a": true, "b": true}}
	if DreamDue(s, t0, DreamEvery, 3) {
		t.Fatal("due with too few sessions")
	}
	s.SessionsSince["c"] = true
	if !DreamDue(s, t0, DreamEvery, 3) {
		t.Fatal("first dream not due")
	}
	s.LastDream = t0.Add(-time.Hour)
	if DreamDue(s, t0, DreamEvery, 3) {
		t.Fatal("due an hour after the last one")
	}

	_, p := newProject(t)
	seed(t, p)
	m := &fakeModel{ops: func(system, prompt string) []Op {
		return []Op{
			{Op: "drop", Key: "status updates", Reason: "superseded by quiet-mode"},
			{Op: "drop", Key: "event log store"}, // no reason: refused
			{Op: "add", Kind: "decision", Key: "invented", Text: "x"},
			{Op: "replace", Kind: "decision", Key: "missing", Text: "x"},
		}
	}}
	st, _ := p.LoadState()
	st.SessionsSince["a"] = true
	p.SaveState(st)
	res, err := Dream(context.Background(), p, m, t0)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := p.LoadRecord()
	if res.Dropped != 1 || res.Skipped != 2 || len(rec.Entries) != 1 || rec.Entries["invented"].Text != "" {
		t.Fatalf("dream = %+v, entries %v", res, rec.Entries)
	}
	if st, _ = p.LoadState(); !st.LastDream.Equal(t0) || len(st.SessionsSince) != 0 {
		t.Fatalf("gate not reset: %+v", st)
	}
}

func TestWorkerDrainsQueue(t *testing.T) {
	home, p := newProject(t)
	dir := t.TempDir()
	stopped := filepath.Join(dir, "a.jsonl")
	writeClaude(t, stopped, "sess-a", p.Root, "Use Postgres.", "OK.")
	idle := filepath.Join(dir, "b.jsonl")
	writeCodex(t, idle, "sess-b", p.Root, "An older session's last words.", "Noted.")
	m := &fakeModel{}
	o := WorkerOptions{Model: m, Now: func() time.Time { return t0 }, NoDream: true}

	// A Stop below the threshold leaves b pending; a later SessionStart flushes it.
	Enqueue(home, Job{Event: "Stop", Transcript: idle, Root: p.Root})
	if err := Work(context.Background(), home, o); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.LoadState(); st.Cursors[idle] == nil || st.Cursors[idle].Pending == 0 {
		t.Fatalf("idle transcript not pending")
	}
	Enqueue(home, Job{Event: "Stop", Transcript: stopped, Root: p.Root, Force: true})
	Enqueue(home, Job{Event: "Stop", Transcript: stopped, Root: p.Root})
	Enqueue(home, Job{Event: "SessionStart", Root: p.Root, Flush: true})
	if err := Work(context.Background(), home, o); err != nil {
		t.Fatal(err)
	}
	st, _ := p.LoadState()
	if st.Cursors[stopped].Extracted != 1 || st.Cursors[idle].Extracted != 1 {
		t.Fatalf("cursors = %+v %+v", st.Cursors[stopped], st.Cursors[idle])
	}
	if left, _ := pendingJobs(home); len(left) != 0 {
		t.Fatalf("queue not drained: %v", left)
	}

	// A second worker while one holds the lock exits at once and leaves the job.
	unlock, err := lockFile(filepath.Join(home, "worker.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	Enqueue(home, Job{Event: "Stop", Transcript: stopped, Root: p.Root})
	if err := Work(context.Background(), home, o); err != nil {
		t.Fatal(err)
	}
	if left, _ := pendingJobs(home); len(left) != 1 {
		t.Fatalf("locked-out worker touched the queue: %v", left)
	}
	unlock()
}

func TestInstallConfig(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		out, err := InstallConfig(agent, "/opt/my bin/spin")
		if err != nil {
			t.Fatal(err)
		}
		start, end := strings.Index(out, "{"), strings.LastIndex(out, "\n}")
		var cfg struct {
			AutoMemory *bool `json:"autoMemoryEnabled"`
			Hooks      map[string][]struct {
				Hooks []map[string]any `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(out[start:end+2]), &cfg); err != nil {
			t.Fatalf("%s config is not JSON: %v\n%s", agent, err, out)
		}
		for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "PreCompact", "SessionEnd"} {
			h := cfg.Hooks[ev]
			if len(h) != 1 || h[0].Hooks[0]["command"] != "'/opt/my bin/spin' hook --agent "+agent {
				t.Fatalf("%s %s = %+v", agent, ev, h)
			}
		}
		if cfg.Hooks["Stop"][0].Hooks[0]["async"] != true {
			t.Fatalf("%s Stop hook is not async", agent)
		}
		if (agent == "claude") != (cfg.AutoMemory != nil && !*cfg.AutoMemory) {
			t.Fatalf("%s autoMemoryEnabled = %v", agent, cfg.AutoMemory)
		}
	}
	if _, err := InstallConfig("cursor", "spin"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}
