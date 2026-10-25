package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Event is one line of a project's activity log, events.jsonl: what memory
// did, when and for which session. It is for people reading along (spin log);
// nothing reads it back into context.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // start, select, recall, skip, mcp, extract, dream
	Agent   string    `json:"agent,omitempty"`
	Session string    `json:"session,omitempty"`
	Name    string    `json:"name,omitempty"` // MCP tool, or what was skipped
	Prompt  string    `json:"prompt,omitempty"`
	Files   []string  `json:"files,omitempty"`
	Ms      int64     `json:"ms,omitempty"`
	Note    string    `json:"note,omitempty"`
	Err     string    `json:"err,omitempty"`
}

const eventPromptChars = 300

func (p Project) eventsPath() string { return filepath.Join(p.Dir, "events.jsonl") }

// Log appends an event, best effort: a full disk must not break a hook.
func (p Project) Log(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.At = e.At.UTC()
	e.Prompt = clipRunes(oneLine(e.Prompt), eventPromptChars)
	if p.Init() == nil {
		appendJSONL(p.eventsPath(), []Event{e})
	}
}

// Events reads the activity log, oldest first.
func (p Project) Events() ([]Event, error) { return readJSONL[Event](p.eventsPath()) }

// String renders an event as one line for spin log.
func (e Event) String() string {
	var b strings.Builder
	sess := e.Session
	if len(sess) > 8 {
		sess = sess[:8]
	}
	fmt.Fprintf(&b, "%s  %-6s %-8s %-7s", e.At.Local().Format("01-02 15:04:05"), e.Agent, sess, e.Kind)
	if e.Name != "" {
		b.WriteString(" " + e.Name)
	}
	if e.Prompt != "" {
		fmt.Fprintf(&b, " %q", clipRunes(e.Prompt, 80))
	}
	if len(e.Files) > 0 {
		b.WriteString(" → " + strings.Join(e.Files, ", "))
	} else if e.Kind == "select" || e.Kind == "recall" {
		b.WriteString(" → nothing")
	}
	if e.Note != "" {
		b.WriteString(" · " + e.Note)
	}
	if e.Ms > 0 {
		fmt.Fprintf(&b, " (%.1fs)", float64(e.Ms)/1000)
	}
	if e.Err != "" {
		b.WriteString(" ERROR " + e.Err)
	}
	return b.String()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return clipRunes(err.Error(), 300)
}
