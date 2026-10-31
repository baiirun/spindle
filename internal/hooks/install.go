package hooks

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed opencode.js
var opencodePlugin string

// InstallConfig returns the hook and MCP config for one tool, to merge by
// hand. Claude Code and Codex take the same hooks shape; one `spin hook`
// command serves every event, reading hook_event_name from stdin. OpenCode
// gets a plugin that sends its events to that same command.
func InstallConfig(agent, bin string) (string, error) {
	if agent == "opencode" {
		b, _ := json.Marshal(bin)
		return strings.ReplaceAll(opencodePlugin, "__SPIN__", string(b)), nil
	}
	if agent != "claude" && agent != "codex" {
		return "", fmt.Errorf("unknown agent %q: want claude, codex or opencode", agent)
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

// LaunchdLabel names the dream schedule's launchd job.
const LaunchdLabel = "com.spindle.dream"

// LaunchdConfig is a launchd agent that runs `spin dream --due` every hour.
// Each project's gate (24 h and 5 sessions since its last dream) decides
// whether anything runs, so most hours it exits at once.
func LaunchdConfig(bin, home string) string {
	path := "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!-- macOS: save as ~/Library/LaunchAgents/%[1]s.plist, then
       launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/%[1]s.plist
     Add the folders holding codex and claude to PATH below if they live
     elsewhere (which codex claude). Undo: launchctl bootout gui/$(id -u)/%[1]s -->
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%[1]s</string>
  <key>ProgramArguments</key>
  <array><string>%[2]s</string><string>dream</string><string>--due</string></array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>%[3]s</string></dict>
  <key>StartInterval</key><integer>3600</integer>
  <key>RunAtLoad</key><true/>
  <key>LowPriorityIO</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%[4]s/dream.log</string>
  <key>StandardErrorPath</key><string>%[4]s/dream.log</string>
</dict>
</plist>
`, LaunchdLabel, xmlEscape(bin), path, xmlEscape(home))
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
