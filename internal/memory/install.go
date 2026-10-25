package memory

import (
	"encoding/json"
	"fmt"
	"strings"
)

// InstallConfig returns the hook and MCP config for one tool, to merge into
// its settings by hand. Both tools take the same hooks shape; one `spin hook`
// command serves every event, reading hook_event_name from stdin.
func InstallConfig(agent, bin string) (string, error) {
	cmd := shellQuote(bin) + " hook --agent " + agent
	handler := func(timeout int, async bool) []map[string]any {
		h := map[string]any{"type": "command", "command": cmd, "timeout": timeout}
		if async {
			h["async"] = true
		}
		return []map[string]any{{"hooks": []map[string]any{h}}}
	}
	hooks := map[string]any{
		"SessionStart":     handler(10, false),
		"UserPromptSubmit": handler(5, false),
		"Stop":             handler(30, true),
		"PreCompact":       handler(10, false),
		"SessionEnd":       handler(2, false), // Codex caps SessionEnd at 3 s
	}
	switch agent {
	case "claude":
		b, _ := json.MarshalIndent(map[string]any{"autoMemoryEnabled": false, "hooks": hooks}, "", "  ")
		return fmt.Sprintf(`# Claude Code: merge into ~/.claude/settings.json (or a project's .claude/settings.json).
# autoMemoryEnabled: false turns off Claude Code's own memory so the two don't compete.
%s

# MCP tools (memory_search, memory_write, transcript_search, ...):
claude mcp add --scope user spindle -- %s mcp
`, b, shellQuote(bin)), nil
	case "codex":
		b, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
		return fmt.Sprintf(`# Codex: merge into ~/.codex/hooks.json (or a trusted repo's .codex/hooks.json),
# then review and trust the hooks with /hooks. Codex's own memories are off by default.
%s

# MCP tools: add to ~/.codex/config.toml
[mcp_servers.spindle]
command = %q
args = ["mcp"]
`, b, bin), nil
	}
	return "", fmt.Errorf("unknown agent %q: want claude or codex", agent)
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\!*?&;|<>()[]{}#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
