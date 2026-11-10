// Package hooks connects Claude Code and Codex hook events to memory. Both tools send the same JSON on stdin (session_id,
// transcript_path, cwd, hook_event_name and per-event fields) and read the
// same hookSpecificOutput.additionalContext back, so one handler serves both.
// OpenCode's plugin (opencode.js) sends its events in the same shape.
//
//	SessionStart  the index: instructions, rules, timelines, wiki names, other projects
//	Stop          queue ingest (async)
//	PreCompact    queue ingest, so memory catches up before context is lost
//	SessionEnd    same; the tools allow 1-3 s here, so it only queues
//
// Every other event is ignored, so an older install that still sends
// UserPromptSubmit or PostToolUse costs a process start and nothing more.
//
// A hook never fails the session: errors go to <SPINDLE_HOME>/hooks.log and
// the hook exits 0.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"spindle/internal/memory"
	"spindle/internal/transcript"
)

// Input is the union of the fields read from either tool.
type Input struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Event          string `json:"hook_event_name"`
	Source         string `json:"source"` // SessionStart: startup, resume, clear, compact
	Prompt         string `json:"prompt"` // UserPromptSubmit
}

// Output is the context-carrying reply both tools accept.
type Output struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Handler runs hook events.
type Handler struct {
	Home  string
	Agent string // claude, codex or opencode; empty: detect from the transcript
	// Spawn starts a detached `spin` with these arguments.
	Spawn func(args ...string) error
}

// Run reads one event from in and writes any reply to out.
func (h Handler) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if os.Getenv(memory.InnerEnv) != "" {
		return nil // a side-model call spindle started: never recurse
	}
	var ev Input
	if err := json.NewDecoder(in).Decode(&ev); err != nil {
		return fmt.Errorf("hook input: %w", err)
	}
	text, err := h.Handle(ctx, ev)
	if text != "" {
		var o Output
		o.HookSpecificOutput.HookEventName = ev.Event
		o.HookSpecificOutput.AdditionalContext = text
		if werr := json.NewEncoder(out).Encode(o); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}

// Handle runs one event and returns the context to add, if any.
func (h Handler) Handle(ctx context.Context, ev Input) (string, error) {
	p, err := memory.Hint(h.Home, ev.Cwd)
	if err != nil {
		return "", err
	}
	agent := h.Agent
	if agent == "" && ev.TranscriptPath != "" {
		agent, _ = transcript.Detect(ev.TranscriptPath)
	}
	switch ev.Event {
	case "SessionStart":
		text, err := p.Index(time.Now())
		if err != nil {
			return "", err
		}
		p.Log(memory.Event{Kind: "start", Agent: agent, Session: ev.SessionID, Note: fmt.Sprintf("%s; index, %d bytes", orDefault(ev.Source, "startup"), len(text))})
		return text, nil
	case "Stop", "PreCompact", "SessionEnd":
		if ev.TranscriptPath == "" {
			return "", nil
		}
		j := memory.Job{Agent: agent, Session: ev.SessionID, Transcript: ev.TranscriptPath, Cwd: p.Cwd, Project: p.Name}
		if err := memory.Enqueue(h.Home, j); err != nil {
			return "", err
		}
		return "", h.spawn("worker")
	}
	return "", nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (h Handler) spawn(args ...string) error {
	if h.Spawn == nil {
		return nil
	}
	return h.Spawn(args...)
}

func (h Handler) logErr(err error) {
	if err != nil {
		Logf(h.Home, "%v", err)
	}
}

// Logf appends a line to <home>/hooks.log, best effort.
func Logf(home, format string, args ...any) {
	if os.MkdirAll(home, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "hooks.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
