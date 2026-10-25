// Package hooks connects Claude Code and Codex hook events to project
// memory. Both tools send the same JSON on stdin (session_id,
// transcript_path, cwd, hook_event_name and per-event fields) and read the
// same hookSpecificOutput.additionalContext back, so one handler serves both.
//
//	SessionStart      instructions and the MEMORY.md index; after compaction, recall may repeat
//	UserPromptSubmit  start the selector; wait a few seconds and attach its picks if they are ready
//	PostToolUse       attach the picks if they arrived after the prompt went in
//	Stop              drop picks that never arrived; queue extraction (async)
//	PreCompact        queue extraction, so memory catches up before context is lost
//	SessionEnd        same; the tools allow 1-3 s here, so it only queues
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
	Agent string // claude or codex; empty: detect from the transcript
	// Spawn starts a detached `spin` with these arguments.
	Spawn func(args ...string) error
	// RecallWait is how long the prompt hook waits for the selector.
	RecallWait time.Duration
	// LocalRecall picks by word overlap in the hook itself, with no model.
	LocalRecall bool
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
	p, err := memory.Resolve(h.Home, ev.Cwd)
	if err != nil {
		return "", err
	}
	agent := h.Agent
	if agent == "" && ev.TranscriptPath != "" {
		agent, _ = transcript.Detect(ev.TranscriptPath)
	}
	switch ev.Event {
	case "SessionStart":
		if ev.Source == "compact" || ev.Source == "clear" {
			h.logErr(p.ResetRecall(ev.SessionID))
		}
		return p.StartContext(), nil
	case "UserPromptSubmit":
		id, err := p.StartRecall(ev.SessionID, ev.Prompt, time.Now())
		if err != nil || id == "" {
			return "", err
		}
		if h.LocalRecall {
			if err := p.RunSelect(ctx, ev.SessionID, id, nil, true); err != nil {
				return "", err
			}
			return p.WaitRecall(ev.SessionID, id, 0)
		}
		if err := h.spawn("select", "--root", p.Root, "--session", ev.SessionID, "--id", id, "--agent", agent); err != nil {
			return "", err
		}
		return p.WaitRecall(ev.SessionID, id, h.RecallWait)
	case "PostToolUse":
		return p.AttachPending(ev.SessionID)
	case "Stop", "PreCompact", "SessionEnd":
		if ev.Event == "Stop" {
			h.logErr(p.DropPending(ev.SessionID))
		}
		if ev.TranscriptPath == "" {
			return "", nil
		}
		j := memory.Job{Agent: agent, Session: ev.SessionID, Transcript: ev.TranscriptPath, Root: p.Root}
		if err := memory.Enqueue(h.Home, j); err != nil {
			return "", err
		}
		return "", h.spawn("worker")
	}
	return "", nil
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
