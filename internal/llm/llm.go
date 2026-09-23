// Package llm runs models through the headless Claude Code CLI (`claude -p`).
// Using the CLI keeps auth on the user's existing login and gives agent runs
// (with tools) and plain completions (without tools) the same interface.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Models used in v0. They're frozen here so eval runs stay comparable.
const (
	Extractor = "claude-sonnet-5"
	Reader    = "claude-sonnet-5"
	Judge     = "claude-opus-5-5"
	Labeler   = "claude-opus-5-5"
)

// Request is one headless run.
type Request struct {
	Model    string
	System   string
	Prompt   string
	Tools    []string // empty means no tools
	Dir      string   // working directory for agent runs
	MaxTurns int
	Schema   string   // optional JSON schema for structured output
	Deny     []string // permission deny rules, e.g. "Read(//Users/**)"
	Timeout  time.Duration
}

// Result is the parsed outcome plus the trace needed for behavioral scoring.
type Result struct {
	Text       string          `json:"text"`
	Structured json.RawMessage `json:"structured,omitempty"`
	CostUSD    float64         `json:"cost_usd"`
	Turns      int             `json:"turns"`
	DurationMS int64           `json:"duration_ms"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	IsError    bool            `json:"is_error"`
}

// ToolCall is one tool invocation seen in the stream.
type ToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type streamEvent struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	NumTurns         int             `json:"num_turns"`
	DurationMS       int64           `json:"duration_ms"`
	IsError          bool            `json:"is_error"`
	Message          struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// Run executes a request and returns its result.
func Run(ctx context.Context, r Request) (Result, error) {
	if r.Timeout == 0 {
		r.Timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	args := []string{
		"-p", "--model", r.Model,
		"--output-format", "stream-json", "--verbose",
		"--setting-sources", "", "--strict-mcp-config",
		"--system-prompt", r.System,
		"--tools", strings.Join(r.Tools, ","),
	}
	if len(r.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(r.Tools, ","), "--permission-mode", "dontAsk")
	}
	if len(r.Deny) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, r.Deny...)
	}
	if r.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprint(r.MaxTurns))
	}
	if r.Schema != "" {
		args = append(args, "--json-schema", r.Schema)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = r.Dir
	cmd.Stdin = strings.NewReader(r.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	var res Result
	sawResult := false
	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		var ev streamEvent
		if len(line) == 0 || json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" {
					res.ToolCalls = append(res.ToolCalls, ToolCall{Name: c.Name, Input: c.Input})
				}
			}
		case "result":
			sawResult = true
			res.Text, res.Structured = ev.Result, ev.StructuredOutput
			res.CostUSD, res.Turns, res.DurationMS, res.IsError = ev.TotalCostUSD, ev.NumTurns, ev.DurationMS, ev.IsError
		}
	}
	if !sawResult {
		return res, fmt.Errorf("claude -p produced no result (err=%v): %s", runErr, truncate(stderr.String(), 500))
	}
	if res.IsError || isLimitMessage(res.Text) {
		if isLimitMessage(res.Text) || (res.IsError && strings.Contains(strings.ToLower(res.Text), "limit")) {
			return res, fmt.Errorf("%w: %s", ErrUsageLimit, truncate(res.Text, 200))
		}
		return res, fmt.Errorf("claude -p error: %s", truncate(res.Text, 300))
	}
	return res, nil
}

// ErrUsageLimit means the account hit its usage limit; callers should stop
// rather than burn through the remaining work with failing calls.
var ErrUsageLimit = errors.New("usage limit reached")

// isLimitMessage matches the CLI's limit notice, which arrives as a normal
// result text rather than an error.
func isLimitMessage(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "You've hit your") || strings.HasPrefix(t, "You’ve hit your")
}

// JSON runs a tool-less request that must return an object matching schema,
// and decodes it into out.
func JSON(ctx context.Context, model, system, prompt, schema string, out any) (Result, error) {
	res, err := Run(ctx, Request{Model: model, System: system, Prompt: prompt, Schema: schema})
	if err != nil {
		return res, err
	}
	payload := res.Structured
	if len(payload) == 0 || string(payload) == "null" {
		payload = []byte(extractJSON(res.Text))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return res, fmt.Errorf("decode structured output: %w (text: %s)", err, truncate(res.Text, 300))
	}
	return res, nil
}

// extractJSON finds the outermost JSON object in free text, tolerating fences.
func extractJSON(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
