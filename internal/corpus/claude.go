package corpus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type claudeLine struct {
	Type        string    `json:"type"`
	SessionID   string    `json:"sessionId"`
	Cwd         string    `json:"cwd"`
	Timestamp   time.Time `json:"timestamp"`
	IsSidechain bool      `json:"isSidechain"`
	IsMeta      bool      `json:"isMeta"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// ClaudeSessionPaths lists top-level session transcripts under ~/.claude/projects.
func ClaudeSessionPaths(projectsDir string) ([]string, error) {
	return filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
}

// ReadClaudeSession parses one Claude Code transcript, skipping sidechains
// (subagents) and harness meta messages.
func ReadClaudeSession(path string) (Session, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false, err
	}
	defer f.Close()

	s := Session{Source: "claude", Path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		var l claudeLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue
		}
		if (l.Type != "user" && l.Type != "assistant") || l.IsSidechain || l.IsMeta {
			continue
		}
		if s.ID == "" {
			s.ID, s.Cwd = l.SessionID, l.Cwd
		}
		for _, it := range claudeItems(l) {
			it.ID, it.Line, it.Time = ItemID("claude", s.ID, line), line, l.Timestamp
			s.Items = append(s.Items, it)
		}
	}
	if err := sc.Err(); err != nil {
		return Session{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return s, s.ID != "", nil
}

func claudeItems(l claudeLine) []Item {
	var str string
	if json.Unmarshal(l.Message.Content, &str) == nil {
		if l.Type == "user" && isInjected(str) {
			return nil
		}
		return []Item{{Role: Role(l.Type), Text: str}}
	}
	var blocks []claudeBlock
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return nil
	}
	var out []Item
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if l.Type == "user" && isInjected(b.Text) {
				continue
			}
			out = append(out, Item{Role: Role(l.Type), Text: b.Text})
		case "tool_use":
			out = append(out, Item{Role: RoleTool, Text: "call " + b.Name + " " + truncate(oneLine(string(b.Input)), toolCallMaxChars)})
		case "tool_result":
			out = append(out, Item{Role: RoleTool, Text: "→ " + truncate(oneLine(toolResultText(b.Content)), toolOutputMaxChars)})
		}
	}
	return out
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}
