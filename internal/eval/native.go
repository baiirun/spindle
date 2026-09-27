package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindle/internal/llm"
)

// The native arm is the baseline spindle has to beat: keep going in the same
// Codex thread. Codex compaction summaries are encrypted, so they can't be read
// or graded as text, but resuming a copy of the thread cut at the trial's
// moment replays them to the model exactly as a real continuation would.

// NativePrior returns the Codex session a native trial resumes, or "" when the
// trial has no Codex prior (Claude threads have no equivalent here yet).
func NativePrior(t Trial) string {
	for _, h := range t.Prior {
		if source, session, ok := strings.Cut(h, ":"); ok && source == "codex" {
			return session
		}
	}
	return ""
}

func nativeBrief(ctx context.Context, o TrialRunOptions, t Trial, r *TrialResult) error {
	session := NativePrior(t)
	if session == "" {
		return errors.New("native: trial has no Codex prior session")
	}
	src, err := findCodexRollout(session)
	if err != nil {
		return err
	}
	home := filepath.Join(o.ScratchDir, t.ID)
	defer os.RemoveAll(home)
	rollout := filepath.Join(home, "codex", "sessions", "2000", "01", "01", filepath.Base(src))
	if err := copyRolloutBefore(src, rollout, t.AskedAt); err != nil {
		return err
	}
	if err := os.Symlink(filepath.Join(codexHome(), "auth.json"), filepath.Join(home, "codex", "auth.json")); err != nil {
		return err
	}
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}

	prompt := fmt.Sprintf("%s\n\nUse only what is already in this conversation. Do not run commands or read files; "+
		"your only job is to %s, not to do the work itself.\n\n%s", trialUserTurn(t), trialJob(t), finalInstruction(t))
	started := time.Now()
	res, err := llm.Resume(ctx, llm.ResumeRequest{
		Home: filepath.Join(home, "codex"), SessionID: session, Prompt: prompt,
		Model: llm.Reader, Dir: work, Timeout: 10 * time.Minute,
	})
	r.Brief, r.ToolCalls, r.DurationMS = res.Text, res.ToolCalls, time.Since(started).Milliseconds()
	for _, c := range res.ToolCalls {
		var ev commandEvent
		if json.Unmarshal(c.Input, &ev) == nil && ev.Item.Command != "" {
			r.OtherCalls = append(r.OtherCalls, ev.Item.Command)
		}
	}
	// The stream reports the thread's cumulative usage; the rollout's last
	// token_count holds this turn's.
	r.InputTokens, r.OutputTokens = lastTurnUsage(rollout)
	return err
}

func codexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func findCodexRollout(session string) (string, error) {
	var found string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		filepath.WalkDir(filepath.Join(codexHome(), dir), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, session+".jsonl") {
				found = p
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf("native: no Codex rollout for session %s", session)
}

// copyRolloutBefore writes the rollout lines stamped before cutoff: the thread
// as it stood at the trial's moment.
func copyRolloutBefore(src, dst string, cutoff time.Time) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	kept := 0
	for sc.Scan() {
		var line struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.Timestamp.IsZero() || !line.Timestamp.Before(cutoff) {
			continue
		}
		w.Write(sc.Bytes())
		w.WriteByte('\n')
		kept++
	}
	if err := sc.Err(); err != nil {
		out.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return err
	}
	if kept == 0 {
		out.Close()
		return errors.New("native: no rollout lines before the cutoff")
	}
	return out.Close()
}

func lastTurnUsage(rollout string) (in, out int64) {
	f, err := os.Open(rollout)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		if !strings.Contains(sc.Text(), `"token_count"`) {
			continue
		}
		var ev struct {
			Payload struct {
				Type string `json:"type"`
				Info struct {
					Last struct {
						Input  int64 `json:"input_tokens"`
						Output int64 `json:"output_tokens"`
					} `json:"last_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Payload.Type == "token_count" {
			in, out = ev.Payload.Info.Last.Input, ev.Payload.Info.Last.Output
		}
	}
	return in, out
}
