package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindle/internal/model"
)

var t0 = time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)

// project is a store named proj, linked to a fresh folder, as its session context.
func project(t *testing.T) Project {
	t.Helper()
	t.Setenv(ProjectEnv, "")
	home, root := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	if err := Store(home, "proj").Link(root); err != nil {
		t.Fatal(err)
	}
	p, err := Hint(home, root)
	if err != nil || p.Name != "proj" {
		t.Fatal(p, err)
	}
	return p
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

// replies is a side model that answers each call with the next reply and
// records the prompts it saw.
type replies struct {
	out     []string
	prompts []string
}

func (r *replies) Complete(ctx context.Context, system, prompt, schema string) (string, error) {
	r.prompts = append(r.prompts, prompt)
	if len(r.out) == 0 {
		return "", context.DeadlineExceeded
	}
	next := r.out[0]
	r.out = r.out[1:]
	return next, nil
}

var _ model.Model = (*replies)(nil)

// entry is a parsed log entry for tests.
func entry(t *testing.T, id, session string, at time.Time, gist string) Entry {
	t.Helper()
	g, err := ParseGist(gist)
	if err != nil {
		t.Fatal(err)
	}
	return Entry{ID: EntryID(id), At: at, Session: session, Source: Pointer{Kind: FromCLI}, Gist: g}
}
