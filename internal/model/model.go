// Package model runs the side-model calls memory needs (extraction, recall
// selection, dreaming) through the coding agent CLIs the user already has
// signed in: `claude -p` or `codex exec`. No API keys of spindle's own.
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
	"time"
)

// Model answers one prompt. With a schema the answer is a JSON object.
type Model interface {
	Complete(ctx context.Context, system, prompt, schema string) (string, error)
}

// Role picks the model size: selection has to be fast, extraction and
// dreaming have to be careful.
type Role string

const (
	Select  Role = "select"
	Extract Role = "extract"
	Dream   Role = "dream"
)

// Env overrides. SPINDLE_BACKEND is claude or codex; SPINDLE_MODEL_<ROLE>
// (SELECT, EXTRACT, DREAM) names the model.
const BackendEnv = "SPINDLE_BACKEND"

// Defaults per backend. Claude Code's own memory uses Sonnet to select and
// the main model to extract; so does this. Through the CLI, Sonnet 5.5 starts
// about as fast as Haiku 5.5 (3.0 s against 2.6 s for a one-word reply).
// Codex's memory uses its small model at low effort to extract and more
// effort to consolidate.
var defaults = map[string]map[Role]string{
	"claude": {Select: "sonnet", Extract: "sonnet", Dream: "opus"},
	"codex":  {Select: "gpt-6-luna", Extract: "gpt-6-luna", Dream: "gpt-6-luna"},
}

// For returns the model for a role. backend is the agent whose hook started
// the work, used unless SPINDLE_BACKEND overrides it.
func For(backend string, role Role) Model {
	if b := strings.TrimSpace(os.Getenv(BackendEnv)); b != "" {
		backend = b
	}
	if backend != "codex" {
		backend = "claude"
	}
	name := strings.TrimSpace(os.Getenv("SPINDLE_MODEL_" + strings.ToUpper(string(role))))
	if name == "" {
		name = defaults[backend][role]
	}
	if backend == "codex" {
		effort := "low"
		if role == Dream {
			effort = "medium"
		}
		return Codex{Model: name, Effort: effort}
	}
	return Claude{Model: name}
}

// Claude runs `claude -p` with no tools, no MCP servers, no settings (so no
// hooks) and no saved session.
type Claude struct{ Model string }

func (c Claude) Complete(ctx context.Context, system, prompt, schema string) (string, error) {
	args := []string{"-p", "--model", c.Model, "--system-prompt", system, "--tools", "", "--output-format", "json",
		"--no-session-persistence", "--strict-mcp-config", "--setting-sources", ""}
	if schema != "" {
		args = append(args, "--json-schema", schema)
	}
	out, err := run(ctx, os.TempDir(), "claude", args, prompt)
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
	if len(res.StructuredOutput) > 0 && string(res.StructuredOutput) != "null" {
		return string(res.StructuredOutput), nil
	}
	if schema != "" {
		return JSONObject(res.Result), nil
	}
	return res.Result, nil
}

type claudeResult struct {
	Type             string          `json:"type"`
	Result           string          `json:"result"`
	IsError          bool            `json:"is_error"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// parseClaude reads `claude -p --output-format json`: one result object, or,
// when the user's settings turn on verbose (which --setting-sources "" does
// not stop), an array of every event, whose last "result" event is the answer.
func parseClaude(out []byte) (claudeResult, error) {
	var res claudeResult
	out = bytes.TrimSpace(out)
	if len(out) > 0 && out[0] == '[' {
		var evs []claudeResult
		if err := json.Unmarshal(out, &evs); err != nil {
			return res, fmt.Errorf("claude output: %w", err)
		}
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Type == "result" {
				return evs[i], nil
			}
		}
		return res, fmt.Errorf("claude output: no result event in %d events", len(evs))
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return res, fmt.Errorf("claude output: %w", err)
	}
	return res, nil
}

// Codex runs `codex exec` read-only and ephemeral.
type Codex struct{ Model, Effort string }

func (c Codex) Complete(ctx context.Context, system, prompt, schema string) (string, error) {
	dir := os.TempDir()
	args := []string{"exec", "--ephemeral", "--skip-git-repo-check", "-C", dir, "--sandbox", "read-only", "--json",
		"--model", c.Model, "-c", fmt.Sprintf("model_reasoning_effort=%q", c.Effort)}
	if schema != "" {
		f, err := os.CreateTemp("", "spindle-schema-*.json")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		f.WriteString(schema)
		f.Close()
		args = append(args, "--output-schema", f.Name())
	}
	args = append(args, "-")
	out, err := run(ctx, dir, "codex", args, "<instructions>\n"+system+"\n</instructions>\n\n"+prompt)
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
		return "", errors.New("codex: no answer")
	}
	if schema != "" {
		return JSONObject(text), nil
	}
	return text, nil
}

func run(ctx context.Context, dir, bin string, args []string, stdin string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
	}
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

// JSONObject finds the outermost JSON object in free text, tolerating fences.
func JSONObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Func adapts a function, for tests.
type Func func(ctx context.Context, system, prompt, schema string) (string, error)

func (f Func) Complete(ctx context.Context, system, prompt, schema string) (string, error) {
	return f(ctx, system, prompt, schema)
}
