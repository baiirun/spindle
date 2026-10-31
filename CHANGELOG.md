# Changelog

## OpenCode

- `spin install opencode` prints a plugin for OpenCode 2 to save as `~/.config/opencode/plugins/spindle.js`, with
  the `mcp` entry for `opencode.json` in its header. It sends OpenCode's session hooks to `spin hook --agent
  opencode`: the start context goes in the system prompt, and picks go after the message they came with.
- The plugin appends each session's messages to `~/.spindle/opencode/<session>.jsonl`, which extraction reads.
  Subagent sessions are skipped.
- `scripts/check-opencode-api.sh [VERSION]` checks that OpenCode's published plugin API still has what the plugin
  uses.

## Sources

- Sessions come from sources: `claude-code` and `codex` to start, listed in `~/.spindle/sources.json` (both when
  the file is missing). Extraction and dreams read sessions through them.
- Each worker run, and the hourly `spin dream --due`, sweeps the sources for sessions no hook reported once they
  have been quiet for 10 minutes. A new source's first sweep reads no history.
- New: `spin sources` (`add KIND [--dir DIR]`, `rm KIND`), `memory_sources`, `memory_source`, and `sweep` events
  in `spin log`. A source starts from when it was added.
- No migration: cursors and dream gates keep their format.

## Projects by meaning (#3)

- Memory is shared across projects. A project is a named store under `~/.spindle/projects/<name>`, and
  `~/.spindle/global` loads everywhere. Memories are named `<project>/<name>`, and recall, search and extraction
  read every project.
- Folders link to projects (`spin project NAME --link .`, `memory_project`) and only hint at a session's project.
  New: `spin projects`, `memory_projects`, `--project NAME`, `spin log --project`.
- The selector gets the session's folder, project, the projects already recalled from, and recent messages.
- Scheduled dreams: `spin dream --due`, and `spin install launchd` for an hourly job.
- Extraction and dreams use Codex (`gpt-6-luna`) first and fall back to Claude.
- **Migration, automatic:** stores keyed by repository (`projects/<base>-<hash>`) are renamed to
  `projects/<base>` and linked to their old root the first time `spin` runs. The activity log, cursors and recall
  state move to `~/.spindle`.

## One client (#2)

- `memory.Client` behind both the MCP tools and the CLI: `spin show`, `write`, `rm`, `search`, `index`, `status`,
  `history` (with `--restore`), `log` and `dream`, matching `memory_*`.
- The activity log (`events.jsonl`, `spin log -f`) and per-memory history.

## Rewrite (#1)

- A from-scratch memory after Claude Code's auto memory: typed Markdown files (user, directive, project,
  reference), a `MEMORY.md` index, per-message recall by a side model, extraction after each turn, and daily
  dreams. It works through Claude Code and Codex hooks and an MCP server.
- Replaces the episode-based spindle, which is kept at tag `v0-episodes`.
