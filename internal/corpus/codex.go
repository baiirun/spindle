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

type codexLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexMeta struct {
	ID     string          `json:"id"`
	Cwd    string          `json:"cwd"`
	Source json.RawMessage `json:"source"`
}

type codexItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   []codexContent  `json:"content"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	Output    json.RawMessage `json:"output"`
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CodexSessionPaths lists rollout files under a Codex home.
func CodexSessionPaths(codexHome string) ([]string, error) {
	var paths []string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(codexHome, dir)
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return paths, nil
}

// ReadCodexSession parses one rollout. It returns ok=false for subagent
// sessions, which are derived work rather than user-facing sessions.
func ReadCodexSession(path string) (Session, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false, err
	}
	defer f.Close()

	s := Session{Source: "codex", Path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		var l codexLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue // tolerate partially written trailing lines
		}
		switch l.Type {
		case "session_meta":
			if s.ID != "" {
				continue // forks repeat the parent's meta; the first one is ours
			}
			var m codexMeta
			if err := json.Unmarshal(l.Payload, &m); err != nil {
				return Session{}, false, fmt.Errorf("%s:%d: session_meta: %w", path, line, err)
			}
			if len(m.Source) > 0 && m.Source[0] == '{' {
				return Session{}, false, nil // subagent
			}
			s.ID, s.Cwd = m.ID, m.Cwd
		case "response_item":
			if s.ID == "" {
				continue
			}
			var it codexItem
			if err := json.Unmarshal(l.Payload, &it); err != nil {
				continue
			}
			role, text := codexItemText(it)
			if text == "" {
				continue
			}
			s.Items = append(s.Items, Item{ID: ItemID("codex", s.ID, line), Line: line, Time: l.Timestamp, Role: role, Text: text})
		}
	}
	if err := sc.Err(); err != nil {
		return Session{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return s, s.ID != "", nil
}

func codexItemText(it codexItem) (Role, string) {
	switch it.Type {
	case "message":
		var parts []string
		for _, c := range it.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		text := strings.Join(parts, "\n")
		switch it.Role {
		case "user":
			if isInjected(text) {
				return "", ""
			}
			return RoleUser, text
		case "assistant":
			return RoleAssistant, text
		}
	case "function_call":
		return RoleTool, "call " + it.Name + " " + truncate(oneLine(it.Arguments), toolCallMaxChars)
	case "custom_tool_call":
		return RoleTool, "call " + it.Name + " " + truncate(oneLine(it.Input), toolCallMaxChars)
	case "function_call_output", "custom_tool_call_output":
		return RoleTool, "→ " + truncate(oneLine(rawOutputText(it.Output)), toolOutputMaxChars)
	}
	return "", ""
}

// rawOutputText accepts both string outputs and {"output": "..."} objects.
func rawOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Output != "" {
		return o.Output
	}
	var parts []codexContent
	if json.Unmarshal(raw, &parts) == nil {
		var texts []string
		for _, p := range parts {
			texts = append(texts, p.Text)
		}
		return strings.Join(texts, " ")
	}
	return string(raw)
}
