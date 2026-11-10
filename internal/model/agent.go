package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Agent runs one agentic session: a model with tools, working until it is
// done. Dream uses one where a single completion would have to guess what
// to read.
type Agent interface {
	Run(ctx context.Context, s Session) (string, error)
}

// Session is one agent run. The agent may read anything ReadDirs holds (Codex
// may read anywhere), write only through the commands Commands allows, and
// has no network.
type Session struct {
	Dir      string   // the working directory
	System   string   // standing instructions
	Prompt   string   // the task
	Commands []string // shell command prefixes it may run, such as "/usr/local/bin/spin dream write"
	ReadDirs []string // folders beyond Dir its file tools may read
	// WriteDirs are folders a command it runs must be able to write. Codex's
	// sandbox can only grant these whole, not per command.
	WriteDirs []string
}

// AgentFor returns the agent for a role, Codex first and Claude on failure,
// as For does. SPINDLE_BACKEND pins one.
func AgentFor(role Role) Agent {
	override := strings.TrimSpace(os.Getenv("SPINDLE_MODEL_" + strings.ToUpper(string(role))))
	codex := CodexAgent{Model: orDefault(override, defaults["codex"][role]), Effort: "medium"}
	claude := ClaudeAgent{Model: defaults["claude"][role]}
	switch strings.TrimSpace(os.Getenv(BackendEnv)) {
	case "codex":
		return codex
	case "claude":
		return ClaudeAgent{Model: orDefault(override, defaults["claude"][role])}
	}
	return AgentFallback{codex, claude}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// AgentFallback tries each agent in turn until one finishes. A later agent
// sees what an earlier one already did, so a session that failed halfway
// is picked up, not repeated.
type AgentFallback []Agent

func (f AgentFallback) Run(ctx context.Context, s Session) (string, error) {
	var errs []error
	for _, a := range f {
		out, err := a.Run(ctx, s)
		if err == nil {
			return out, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return "", errors.Join(errs...)
}

// ClaudeAgent runs `claude -p` with Read, Grep, Glob and the allowed
// commands only, no MCP servers, no settings and no saved session.
type ClaudeAgent struct{ Model string }

func (c ClaudeAgent) Run(ctx context.Context, s Session) (string, error) {
	allowed := []string{"Read", "Grep", "Glob"}
	for _, cmd := range s.Commands {
		allowed = append(allowed, "Bash("+cmd+":*)")
	}
	args := []string{"-p", "--model", c.Model, "--system-prompt", s.System,
		"--tools", "Bash,Read,Grep,Glob", "--allowedTools", strings.Join(allowed, ","), "--permission-mode", "dontAsk",
		"--output-format", "json", "--no-session-persistence", "--strict-mcp-config", "--setting-sources", ""}
	for _, d := range s.ReadDirs {
		args = append(args, "--add-dir", d)
	}
	out, err := runSession(ctx, s.Dir, "claude", args, s.Prompt)
	if err != nil {
		return "", err
	}
	res, err := parseClaude(out)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("claude: %s", clip(res.Result, 300))
	}
	return strings.TrimSpace(res.Result), nil
}

// CodexAgent runs `codex exec` in the workspace-write sandbox: it reads
// anywhere, writes only Dir and WriteDirs, and has no network.
type CodexAgent struct{ Model, Effort string }

func (c CodexAgent) Run(ctx context.Context, s Session) (string, error) {
	args := []string{"exec", "--ephemeral", "--skip-git-repo-check", "-C", s.Dir, "--sandbox", "workspace-write", "--json",
		"--model", c.Model, "-c", fmt.Sprintf("model_reasoning_effort=%q", c.Effort), "-c", `approval_policy="never"`}
	for _, d := range s.WriteDirs {
		args = append(args, "--add-dir", d)
	}
	args = append(args, "-")
	prompt := "<instructions>\n" + s.System + "\n</instructions>\n\nRun only these commands: " + strings.Join(s.Commands, "; ") + ".\n\n" + s.Prompt
	out, err := runSession(ctx, s.Dir, "codex", args, prompt)
	if err != nil {
		return "", err
	}
	var text string
	for _, line := range bytes.Split(out, []byte("\n")) {
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &ev) == nil && ev.Type == "item.completed" && ev.Item.Type == "agent_message" {
			text = ev.Item.Text
		}
	}
	if text == "" {
		return "", errors.New("codex: the session ended without a final message")
	}
	return strings.TrimSpace(text), nil
}

// runSession runs an agent CLI with no deadline of its own: a dream session
// takes as long as it takes, and ctx is how a caller stops it.
func runSession(ctx context.Context, dir, bin string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", bin, err, clip(stderr.String(), 300))
	}
	return stdout.Bytes(), nil
}

// AgentFunc adapts a function, for tests.
type AgentFunc func(ctx context.Context, s Session) (string, error)

func (f AgentFunc) Run(ctx context.Context, s Session) (string, error) { return f(ctx, s) }
