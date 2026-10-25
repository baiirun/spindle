// Package transcript reads Claude Code and Codex session transcripts into one
// shape: the human's and the assistant's messages, in order, each with the
// transcript line it came from. Tool calls and harness-injected text are left
// out; memory is about what the user and the assistant said.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Message is one user or assistant message.
type Message struct {
	ID   string // <agent>:<first 8 of session>#L<line>, stable because transcripts are append-only
	Line int
	Time time.Time
	Role string // "user" or "assistant"
	Text string
}

// Session is one transcript.
type Session struct {
	Agent    string // "claude" or "codex"
	ID       string
	Cwd      string
	Path     string
	Subagent bool
	Messages []Message
}

// Read parses a transcript, telling the two formats apart by content.
func Read(path string) (Session, error) {
	agent, err := Detect(path)
	if err != nil {
		return Session{}, err
	}
	if agent == "codex" {
		return readCodex(path)
	}
	return readClaude(path)
}

// Detect says which tool wrote a transcript: a Codex rollout's lines carry a
// type and a payload; Claude Code's carry a sessionId.
func Detect(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := scanner(f)
	for i := 0; i < 20 && sc.Scan(); i++ {
		var l struct {
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
			SessionID string          `json:"sessionId"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if len(l.Payload) > 0 {
			return "codex", nil
		}
		if l.SessionID != "" {
			return "claude", nil
		}
	}
	return "claude", nil
}

func scanner(f *os.File) *bufio.Scanner {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	return sc
}

func id(agent, session string, line int) string {
	if len(session) > 8 {
		session = session[:8]
	}
	return fmt.Sprintf("%s:%s#L%d", agent, session, line)
}

// injected marks user-role text the harness added rather than the human typed.
var injected = []string{
	"<environment_context>", "# AGENTS.md instructions", "<user_instructions>", "<permissions",
	"<skill>", "<subagent_notification>", "<turn_aborted>", "<collaboration_mode>", "<system-reminder>",
	"<command-name>", "<local-command", "Caveat: The messages below were generated", "<task-notification>",
	"<user-prompt-submit-hook>", "<session-start-hook>",
}

func isInjected(s string) bool {
	s = strings.TrimSpace(s)
	for _, p := range injected {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func readClaude(path string) (Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer f.Close()
	s := Session{Agent: "claude", Path: path}
	sc := scanner(f)
	for line := 1; sc.Scan(); line++ {
		var l struct {
			Type        string    `json:"type"`
			SessionID   string    `json:"sessionId"`
			Cwd         string    `json:"cwd"`
			Timestamp   time.Time `json:"timestamp"`
			IsSidechain bool      `json:"isSidechain"`
			IsMeta      bool      `json:"isMeta"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || (l.Type != "user" && l.Type != "assistant") {
			continue
		}
		if s.ID == "" {
			s.ID, s.Cwd = l.SessionID, l.Cwd
		}
		if l.IsSidechain {
			s.Subagent = true
			continue
		}
		if l.IsMeta {
			continue
		}
		text := claudeText(l.Message.Content)
		if text == "" || (l.Type == "user" && isInjected(text)) {
			continue
		}
		s.Messages = append(s.Messages, Message{ID: id("claude", s.ID, line), Line: line, Time: l.Timestamp, Role: l.Type, Text: text})
	}
	return s, sc.Err()
}

// claudeText joins a message's text blocks; tool use and tool results are skipped.
func claudeText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.TrimSpace(str)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" && !isInjected(b.Text) {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func readCodex(path string) (Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer f.Close()
	s := Session{Agent: "codex", Path: path}
	sc := scanner(f)
	for line := 1; sc.Scan(); line++ {
		var l struct {
			Timestamp time.Time       `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // a partly written last line
		}
		switch l.Type {
		case "session_meta":
			if s.ID != "" {
				continue // a fork repeats its parent's meta after its own
			}
			var m struct {
				ID     string          `json:"id"`
				Cwd    string          `json:"cwd"`
				Source json.RawMessage `json:"source"`
			}
			if json.Unmarshal(l.Payload, &m) == nil {
				s.ID, s.Cwd = m.ID, m.Cwd
				s.Subagent = len(m.Source) > 0 && m.Source[0] == '{' // sub-agents carry an object source
			}
		case "response_item":
			var it struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(l.Payload, &it) != nil || it.Type != "message" || (it.Role != "user" && it.Role != "assistant") {
				continue
			}
			var parts []string
			for _, c := range it.Content {
				if t := strings.TrimSpace(c.Text); t != "" && !(it.Role == "user" && isInjected(t)) {
					parts = append(parts, t)
				}
			}
			if len(parts) == 0 {
				continue
			}
			s.Messages = append(s.Messages, Message{ID: id("codex", s.ID, line), Line: line, Time: l.Timestamp, Role: it.Role, Text: strings.Join(parts, "\n\n")})
		}
	}
	return s, sc.Err()
}

// Since returns the messages after a line.
func (s Session) Since(line int) []Message {
	for i, m := range s.Messages {
		if m.Line > line {
			return s.Messages[i:]
		}
	}
	return nil
}

// Render writes messages as "[id time role] text" blocks for a prompt.
func Render(ms []Message, maxChars int) string {
	var b strings.Builder
	for _, m := range ms {
		text := m.Text
		if r := []rune(text); maxChars > 0 && len(r) > maxChars {
			text = string(r[:maxChars]) + " …(cut)"
		}
		fmt.Fprintf(&b, "[%s %s %s]\n%s\n\n", m.ID, m.Time.UTC().Format("2006-01-02 15:04Z"), m.Role, text)
	}
	return b.String()
}
