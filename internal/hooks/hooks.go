// Package hooks turns Claude Code and Codex hook events into spindle memory
// work. Both tools send the same JSON on stdin (session_id, transcript_path,
// cwd, hook_event_name, plus per-event fields) and read the same
// hookSpecificOutput.additionalContext back, so one handler serves both.
//
//	SessionStart       inject the Now summary, recent user messages and the record index;
//	                   queue a flush of the project's other unfolded sessions
//	UserPromptSubmit   inject matching record entries and past user messages (local search, bounded)
//	Stop               queue the transcript for extraction (run with async: true)
//	PreCompact         queue a forced extraction, so the Now summary catches up
//	SessionEnd         same; the tools allow 1-3 s here, so it only queues
//
// A hook never fails the session: errors go to <SPINDLE_HOME>/hooks.log and
// the hook exits 0 with no output.
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
)

// Input is the union of the fields spindle reads from either tool.
type Input struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Event          string `json:"hook_event_name"`
	Source         string `json:"source"`  // SessionStart: startup, resume, clear, compact, fork
	Prompt         string `json:"prompt"`  // UserPromptSubmit
	Trigger        string `json:"trigger"` // PreCompact: manual, auto
	Reason         string `json:"reason"`  // SessionEnd
}

// Output is the context-carrying reply both tools accept.
type Output struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Handler runs one hook event.
type Handler struct {
	Home   string // SPINDLE_HOME
	Agent  string // "claude" or "codex"; empty: detect from the transcript
	Spawn  func() error
	Now    func() time.Time
	Recall time.Duration // deadline for the prompt-time search
}

// Run reads one event from in and writes any reply to out.
func (h Handler) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if os.Getenv(memory.InnerEnv) != "" {
		return nil // a model call from spindle's own worker: never recurse
	}
	var ev Input
	if err := json.NewDecoder(in).Decode(&ev); err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	text, err := h.Handle(ctx, ev)
	if err != nil {
		return err
	}
	if text == "" {
		return nil
	}
	var o Output
	o.HookSpecificOutput.HookEventName = ev.Event
	o.HookSpecificOutput.AdditionalContext = text
	return json.NewEncoder(out).Encode(o)
}

// Handle runs the event and returns the context to inject, if any.
func (h Handler) Handle(ctx context.Context, ev Input) (string, error) {
	if h.Now == nil {
		h.Now = time.Now
	}
	p, err := memory.ResolveProject(h.Home, ev.Cwd)
	if err != nil {
		return "", err
	}
	agent := h.Agent
	if agent == "" && ev.TranscriptPath != "" {
		agent = memory.DetectAgent(ev.TranscriptPath)
	}
	job := memory.Job{Event: ev.Event, Agent: agent, Session: ev.SessionID, Transcript: ev.TranscriptPath, Root: p.Root, At: h.Now().UTC()}
	switch ev.Event {
	case "SessionStart":
		// The new session's own transcript has nothing to fold yet; flush the others.
		job.Transcript, job.Flush = "", true
		if err := h.queue(job); err != nil {
			h.logf("queue flush: %v", err)
		}
		return memory.SessionStart(p, ev.Source, ev.SessionID, memory.StartBudget)
	case "UserPromptSubmit":
		return memory.Recall(ctx, p, ev.SessionID, ev.Prompt, memory.RecallBudget, h.Recall)
	case "Stop":
		if ev.TranscriptPath == "" {
			return "", nil
		}
		return "", h.queue(job)
	case "PreCompact", "SessionEnd":
		if ev.TranscriptPath == "" {
			return "", nil
		}
		job.Force = true
		return "", h.queue(job)
	}
	return "", nil
}

func (h Handler) queue(j memory.Job) error {
	if err := memory.Enqueue(h.Home, j); err != nil {
		return err
	}
	if h.Spawn != nil {
		return h.Spawn()
	}
	return nil
}

func (h Handler) logf(format string, args ...any) {
	Logf(h.Home, format, args...)
}

// Logf appends a line to <home>/hooks.log, best effort.
func Logf(home, format string, args ...any) {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "hooks.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
