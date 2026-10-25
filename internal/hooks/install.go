package hooks

import (
	"encoding/json"
	"fmt"
	"strings"
)

// InstallConfig returns the hook and MCP config for one tool, to merge by
// hand. Both take the same hooks shape; one `spin hook` command serves every
// event, reading hook_event_name from stdin.
func InstallConfig(agent, bin string) (string, error) {
	if agent != "claude" && agent != "codex" {
		return "", fmt.Errorf("unknown agent %q: want claude or codex", agent)
	}
	cmd := quote(bin) + " hook --agent " + agent
	handler := func(timeout int, async bool) []map[string]any {
		h := map[string]any{"type": "command", "command": cmd, "timeout": timeout}
		if async {
			h["async"] = true
		}
		return []map[string]any{{"hooks": []map[string]any{h}}}
	}
	hooks := map[string]any{
		"SessionStart":     handler(10, false),
		"UserPromptSubmit": handler(10, false), // waits up to SPINDLE_RECALL_WAIT (4 s) for the selector
		"PostToolUse":      handler(5, false),
		"Stop":             handler(30, true),
		"PreCompact":       handler(10, false),
		"SessionEnd":       handler(2, false), // Codex caps SessionEnd at 3 s
	}
	if agent == "claude" {
		b, _ := json.MarshalIndent(map[string]any{"autoMemoryEnabled": false, "hooks": hooks}, "", "  ")
		return fmt.Sprintf(`# Claude Code: merge into ~/.claude/settings.json.
# autoMemoryEnabled: false turns Claude Code's own memory off, so the two don't both run.
%s

# Memory tools:
claude mcp add --scope user spindle -- %s mcp --agent claude
`, b, quote(bin)), nil
	}
	b, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	return fmt.Sprintf(`# Codex: merge into ~/.codex/hooks.json, then trust the hooks with /hooks.
# Codex's own memories are off by default; leave them off.
%s

# Memory tools: add to ~/.codex/config.toml
[mcp_servers.spindle]
command = %q
args = ["mcp", "--agent", "codex"]
`, b, bin), nil
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\!*?&;|<>()[]{}#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
