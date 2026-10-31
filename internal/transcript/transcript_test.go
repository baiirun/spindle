package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaude(t *testing.T) {
	p := write(t,
		`{"type":"user","sessionId":"abcdef123456","cwd":"/w","timestamp":"2026-10-25T04:00:00Z","message":{"role":"user","content":"Use Postgres."}}`,
		`{"type":"user","sessionId":"abcdef123456","timestamp":"2026-10-25T04:00:01Z","isMeta":true,"message":{"content":"meta"}}`,
		`{"type":"user","sessionId":"abcdef123456","timestamp":"2026-10-25T04:00:02Z","message":{"content":"<system-reminder>x</system-reminder>"}}`,
		`{"type":"assistant","sessionId":"abcdef123456","timestamp":"2026-10-25T04:00:03Z","message":{"content":[{"type":"text","text":"OK."},{"type":"tool_use","name":"Bash","input":{}}]}}`,
		`{"type":"user","sessionId":"abcdef123456","timestamp":"2026-10-25T04:00:04Z","message":{"content":[{"type":"tool_result","content":"out"}]}}`,
		`not json`,
	)
	s, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != "claude" || s.ID != "abcdef123456" || s.Cwd != "/w" || len(s.Messages) != 2 {
		t.Fatalf("session = %+v", s)
	}
	if s.Messages[0].ID != "claude:abcdef12#L1" || s.Messages[1].Text != "OK." || s.Messages[1].Line != 4 {
		t.Fatalf("messages = %+v", s.Messages)
	}
	if got := s.Since(1); len(got) != 1 || got[0].Line != 4 {
		t.Fatalf("Since = %+v", got)
	}
}

func TestCodex(t *testing.T) {
	p := write(t,
		`{"timestamp":"2026-10-25T04:00:00Z","type":"session_meta","payload":{"id":"019e85fe-aaaa","cwd":"/w","source":"cli"}}`,
		`{"timestamp":"2026-10-25T04:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}`,
		`{"timestamp":"2026-10-25T04:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Use d6 damage."}]}}`,
		`{"timestamp":"2026-10-25T04:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell"}}`,
		`{"timestamp":"2026-10-25T04:00:04Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
	)
	s, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != "codex" || s.Subagent || len(s.Messages) != 2 || s.Messages[0].ID != "codex:019e85fe#L3" {
		t.Fatalf("session = %+v", s)
	}
	sub := write(t, `{"timestamp":"2026-10-25T04:00:00Z","type":"session_meta","payload":{"id":"x","source":{"subagent":"review"}}}`)
	if s, _ := Read(sub); !s.Subagent {
		t.Fatal("subagent not flagged")
	}
}

func TestOpencode(t *testing.T) {
	p := write(t,
		`{"type":"session","agent":"opencode","id":"ses_4a1b2c3d4e","cwd":"/w"}`,
		`{"type":"message","mid":"msg_1","role":"user","time":"2026-10-25T04:00:00.000Z","text":"Use Postgres."}`,
		`{"type":"message","role":"assistant","time":"2026-10-25T04:00:01.000Z","text":""}`,
		`{"type":"message","role":"user","time":"2026-10-25T04:00:02.000Z","text":"<system-reminder>x</system-reminder>"}`,
		`{"type":"message","role":"assistant","time":"2026-10-25T04:00:03.000Z","text":"OK."}`,
	)
	s, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != "opencode" || s.ID != "ses_4a1b2c3d4e" || s.Cwd != "/w" || s.Subagent || len(s.Messages) != 2 {
		t.Fatalf("session = %+v", s)
	}
	if s.Messages[0].ID != "opencode:ses_4a1b#L2" || s.Messages[1].Text != "OK." || s.Messages[1].Line != 5 {
		t.Fatalf("messages = %+v", s.Messages)
	}
	sub := write(t, `{"type":"session","agent":"opencode","id":"ses_x","parentID":"ses_y"}`)
	if s, _ := Read(sub); !s.Subagent {
		t.Fatal("subagent not flagged")
	}
}
