package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"spindle/internal/dream"
)

// Project-memory arms. "memory" grades the memory snapshot itself as the brief:
// does the fold alone hold what a continuation needs? "memory-agent" gives a
// fresh agent the snapshot plus spin, to see whether it can use it.
const (
	ArmMemory      = "memory"
	ArmMemoryAgent = "memory-agent"
)

// memorySnapshot returns the project memory folded entirely before the trial's
// moment, without its storage header.
func memorySnapshot(dir string, t Trial) (string, dream.Step, error) {
	b, err := os.ReadFile(filepath.Join(dir, "steps.json"))
	if err != nil {
		return "", dream.Step{}, fmt.Errorf("memory: %w", err)
	}
	var steps []dream.Step
	if err := json.Unmarshal(b, &steps); err != nil {
		return "", dream.Step{}, fmt.Errorf("memory: %w", err)
	}
	s, ok := dream.AsOf(steps, t.AskedAt)
	if !ok {
		return "", s, fmt.Errorf("memory: no snapshot before %s", t.AskedAt.Format("2006-01-02 15:04"))
	}
	text, err := os.ReadFile(s.Path)
	if err != nil {
		return "", s, err
	}
	body := string(text)
	if strings.HasPrefix(body, "---\n") {
		if i := strings.Index(body[4:], "\n---\n"); i >= 0 {
			body = body[4+i+5:]
		}
	}
	return strings.TrimSpace(body), s, nil
}

func memoryBrief(o TrialRunOptions, t Trial, r *TrialResult) error {
	text, s, err := memorySnapshot(o.MemoryDir, t)
	if err != nil {
		return err
	}
	r.Brief, r.MemoryAsOf = text, s.AsOf.UTC().Format("2006-01-02T15:04Z")
	return nil
}

func memoryAgentBrief(ctx context.Context, o TrialRunOptions, t Trial, r *TrialResult) error {
	text, s, err := memorySnapshot(o.MemoryDir, t)
	if err != nil {
		return err
	}
	r.MemoryAsOf = s.AsOf.UTC().Format("2006-01-02T15:04Z")
	if h, err := os.ReadFile(strings.TrimSuffix(s.Path, ".md") + ".history.md"); err == nil {
		text += "\n\n" + string(h) // format v2: history lives in a code-built index
	}
	preface := fmt.Sprintf("Project memory, folded from this project's episodes through %s. It is a summary: it may be\n"+
		"incomplete or slightly stale, so check specifics through its citations (spin read <item>) and read the\n"+
		"transcript after it before relying on anything.\n\n<project_memory>\n%s\n</project_memory>", r.MemoryAsOf, text)
	return spinBriefWith(ctx, o, t, r, preface)
}
