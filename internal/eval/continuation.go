package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"spindle/internal/llm"
)

// ContinuationOptions configures one fresh-agent warm-start trial.
type ContinuationOptions struct {
	CorpusRoot  string
	EpisodeRoot string
	Source      string
	Session     string
	Task        string
	WorkDir     string
	RunDir      string
}

// ContinuationResult records the observable outcome of one warm-start trial.
type ContinuationResult struct {
	Source          string         `json:"source"`
	Session         string         `json:"session"`
	Task            string         `json:"task"`
	Answer          string         `json:"answer"`
	ToolCalls       []llm.ToolCall `json:"tool_calls"`
	AttemptedResume bool           `json:"attempted_resume"`
	UsedResume      bool           `json:"used_resume"`
	DurationMS      int64          `json:"duration_ms"`
	Error           string         `json:"error,omitempty"`
}

// CLIName is the installed command name of the spindle CLI.
const CLIName = "spin"

// RunContinuation starts a fresh agent with a known prior-session handle. The
// agent must use Spindle's read-only commands; the harness only supplies the
// handle and records the resulting trace.
func RunContinuation(ctx context.Context, o ContinuationOptions) (ContinuationResult, error) {
	r := ContinuationResult{Source: o.Source, Session: o.Session, Task: o.Task}
	if o.CorpusRoot == "" || o.EpisodeRoot == "" || o.Source == "" || o.Session == "" || o.Task == "" || o.WorkDir == "" || o.RunDir == "" {
		return r, fmt.Errorf("corpus root, episode root, source, session, task, work directory, and run directory are required")
	}
	if err := os.MkdirAll(o.RunDir, 0o755); err != nil {
		return r, err
	}
	command, err := buildContinuationCommand(ctx, o.WorkDir, o.RunDir)
	if err != nil {
		return r, err
	}
	o.CorpusRoot, err = filepath.Abs(o.CorpusRoot)
	if err != nil {
		return r, err
	}
	o.EpisodeRoot, err = filepath.Abs(o.EpisodeRoot)
	if err != nil {
		return r, err
	}
	started := time.Now()
	res, err := llm.Run(ctx, llm.Request{
		Model: llm.Reader, Dir: o.WorkDir, Timeout: 8 * time.Minute,
		System: continuationSystem(o, command), Prompt: o.Task,
	})
	r.Answer, r.ToolCalls, r.DurationMS = res.Text, res.ToolCalls, time.Since(started).Milliseconds()
	r.AttemptedResume = attemptedResume(r.ToolCalls)
	r.UsedResume = usedResume(r.ToolCalls)
	if err != nil {
		r.Error = err.Error()
	}
	if err := writeContinuation(filepath.Join(o.RunDir, "continuation.json"), r); err != nil {
		return r, err
	}
	if err != nil {
		return r, err
	}
	if !r.UsedResume {
		return r, fmt.Errorf("continuation agent did not successfully invoke %s resume", CLIName)
	}
	return r, nil
}

func buildContinuationCommand(ctx context.Context, workDir, runDir string) (string, error) {
	path, err := filepath.Abs(filepath.Join(runDir, CLIName))
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, "./cmd/"+CLIName)
	cmd.Dir = workDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build continuation command: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return path, nil
}

func continuationSystem(o ContinuationOptions, binary string) string {
	command := fmt.Sprintf("%s resume --episodes %s --source %s --session %s", shellQuote(binary), shellQuote(o.EpisodeRoot), shellQuote(o.Source), shellQuote(o.Session))
	related := fmt.Sprintf("%s related --episodes %s EPISODE_REF", shellQuote(binary), shellQuote(o.EpisodeRoot))
	read := fmt.Sprintf("%s read --episodes %s --corpus %s REF", shellQuote(binary), shellQuote(o.EpisodeRoot), shellQuote(o.CorpusRoot))
	return fmt.Sprintf(`You are a fresh agent taking over an existing effort. You have no remembered context.

The harness supplied a prior-session handle. Before reasoning about the task, run exactly:

  %s

Treat its episode as derived context, not the source of truth. Follow its explicit links with:

  %s

Expand any episode or transcript reference with:

  %s

If the startup packet lists a source-only range, expand that transcript before relying on it.

Do not edit files. State the relevant context you recovered, the next concrete action, and any uncertainty. Cite transcript item IDs where available.`, command, related, read)
}

func attemptedResume(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if resumeCall(call.Input) != nil {
			return true
		}
	}
	return false
}

func usedResume(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if event := resumeCall(call.Input); event != nil && event.Item.ExitCode != nil && *event.Item.ExitCode == 0 {
			return true
		}
	}
	return false
}

type commandEvent struct {
	Item struct {
		Command  string `json:"command"`
		ExitCode *int   `json:"exit_code"`
	} `json:"item"`
}

func resumeCall(input []byte) *commandEvent {
	var event commandEvent
	if json.Unmarshal(input, &event) != nil || !resumeInvocation(event.Item.Command) {
		return nil
	}
	return &event
}

func resumeInvocation(command string) bool {
	for rest := command; ; {
		i := strings.Index(rest, CLIName)
		if i < 0 {
			return false
		}
		after := strings.TrimLeft(rest[i+len(CLIName):], "'\"")
		if strings.HasPrefix(after, " resume") {
			return true
		}
		rest = rest[i+len(CLIName):]
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func writeContinuation(path string, result ContinuationResult) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// BuildCLI builds the spin binary from repo into dir and returns its path, so
// trials run the code under test rather than whatever is installed.
func BuildCLI(ctx context.Context, repo, dir string) (string, error) {
	return buildContinuationCommand(ctx, repo, dir)
}
