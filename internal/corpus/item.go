// Package corpus normalizes agent transcripts into plain-text Markdown chunks
// with stable item IDs. See docs/corpus.md.
package corpus

import (
	"fmt"
	"strings"
	"time"
)

// Role says who produced an item. Attribution in memory depends on keeping
// the human user's words distinct from everything else.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Item is one normalized transcript entry.
type Item struct {
	ID   string // <source>:<session prefix>#L<line>
	Line int    // 1-based line in the source JSONL; stable because sources are append-only
	Time time.Time
	Role Role
	Text string
}

// Session is one root agent session.
type Session struct {
	Source string // "codex" | "claude"
	ID     string
	Cwd    string
	Path   string
	Items  []Item
}

const sessionPrefixLen = 8

// ItemID builds the stable citation ID for a transcript line.
func ItemID(source, sessionID string, line int) string {
	prefix := sessionID
	if len(prefix) > sessionPrefixLen {
		prefix = prefix[:sessionPrefixLen]
	}
	return fmt.Sprintf("%s:%s#L%d", source, prefix, line)
}

// Render formats an item as one chunk line: "[id time role] text".
// Newlines are kept so messages stay readable; the header marks item boundaries.
func (it Item) Render() string {
	return fmt.Sprintf("[%s %s %s] %s", it.ID, it.Time.UTC().Format(time.RFC3339), it.Role, strings.TrimSpace(it.Text))
}

const (
	toolOutputMaxChars = 300
	toolCallMaxChars   = 200
)

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(truncated)"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// injectedPrefixes mark user-role messages that the harness injected rather
// than the human typed. They carry no user intent and would pollute attribution.
var injectedPrefixes = []string{
	"<environment_context>", "# AGENTS.md instructions", "<user_instructions>",
	"<permissions", "<skill>", "<subagent_notification>", "<turn_aborted>",
	"<recommended_plugins>", "<collaboration_mode>", "<codex_internal_context",
	"Here is a list of plugins", "<system-reminder>", "<command-name>",
	"<local-command", "Caveat: The messages below were generated",
	"<task-notification>",
}

func isInjected(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range injectedPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}
