# Using spindle

How to set spindle up, the commands, the settings, where its files live, and what to check when something looks
wrong. For how it works see [architecture.md](architecture.md); for why it works that way see
[design.md](design.md).

## Setup

You need Go, and at least one of the `claude` and `codex` CLIs signed in. Spindle makes its model calls through
them and has no API keys of its own.

```sh
go build -o ~/go/bin/spin ./cmd/spin
```

### Claude Code

```sh
spin install claude
```

This prints a `hooks` block to merge into `~/.claude/settings.json` by hand, plus the command that adds the MCP
server (`claude mcp add --scope user spindle -- ~/go/bin/spin mcp --agent claude`). The printed block also has
`"autoMemoryEnabled": false`, which turns Claude Code's own memory off. Delete that line if you want both memories
to run side by side. Then allow the tools, for example with `"mcp__spindle"` in `permissions.allow`.

### Codex

```sh
spin install codex
```

This prints a block to merge into `~/.codex/hooks.json` and an `[mcp_servers.spindle]` table for
`~/.codex/config.toml`. Codex skips hooks it hasn't trusted. Trust them with `/hooks` in the Codex **CLI**; the
desktop app's `/hooks` does nothing. Trust is stored in `config.toml` under `hooks.state`.

### Dream schedule (macOS)

```sh
spin install launchd > ~/Library/LaunchAgents/com.spindle.dream.plist
plutil -lint ~/Library/LaunchAgents/com.spindle.dream.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.spindle.dream.plist
```

This runs `spin dream --due` every hour and once when it loads. The plist's `PATH` covers `/opt/homebrew/bin`,
`/usr/local/bin`, `/usr/bin` and `/bin`. If `which codex claude` shows them somewhere else, add that folder to it.
Output goes to `~/.spindle/dream.log`. Without the schedule, dreams still run after extraction once they are due.

### Link your folders

```sh
cd ~/code/spindle && spin project spindle --description "Portable agent memory" --link .
```

`--link .` links the current folder's repository root. A folder only hints at which project a session is in;
every project's memories stay visible everywhere. Agents can do the same with `memory_project`.

### Undo

- Remove the `spin hook` entries from `~/.claude/settings.json` and `~/.codex/hooks.json`.
- `claude mcp remove --scope user spindle`, and delete `[mcp_servers.spindle]` from `~/.codex/config.toml`.
- `launchctl bootout gui/$(id -u)/com.spindle.dream`, then delete the plist.
- Memory stays in `~/.spindle` until you delete it.

## Commands

Most commands work out the session's project from the working directory. `--project NAME` overrides that, and
`global` is a valid name. Memories are named `<project>/<name>`. A bare name is looked up in this folder's
project, then in global, then in any project that has it.

| Command | What it does |
|---|---|
| `spin status` | This folder's project: memory counts by type, linked folders, dream gate; counts for global and the rest; the latest transcript cursors. |
| `spin projects` | Every project with its memory count, description and folders. `*` marks this folder's. |
| `spin project NAME [--description D] [--link DIR] [--unlink DIR]` | Create or describe a project, link or unlink a folder. `--link .` is this repository's root. |
| `spin index` | What a session here starts with: this project's index, global, a line per other project. |
| `spin show NAME` | One memory in full. |
| `spin search [-n N] WORDS...` | Memories of every project that match the words, best first. |
| `spin write [--type T] [--description D] [--global] NAME < body.md` | Create or replace a memory; the body comes from stdin. `NAME` may be `project/name`; `--global` saves to global. Type and description can be left out when updating. |
| `spin rm [--reason WHY] NAME` | Delete a memory. |
| `spin history [--full] [--restore N] NAME` | Every change to a memory, newest first. `--full` shows the old text; `--restore N` puts back the version change N replaced or deleted. |
| `spin log [-f] [-n N] [--session S] [--kind K] [--project P] [--json]` | The activity log across projects. `-f` follows it. Kinds: start, select, recall, skip, call, extract, dream. |
| `spin dream [--force]` | Dream this folder's project, if its gate is open (`--force`: anyway). |
| `spin dream --due` | Dream every store whose gate is open. The schedule runs this. |
| `spin extract TRANSCRIPT` | Run one extraction pass over a transcript now. The project comes from the transcript's folder. |
| `spin install claude\|codex\|launchd [--bin PATH]` | Print config to merge by hand. Nothing is written. |

Hooks and background work call these; you don't run them yourself:

| Command | Started by |
|---|---|
| `spin hook --agent claude\|codex` | Every hook event, with the event JSON on stdin |
| `spin mcp --agent claude\|codex` | The agent, as its MCP server |
| `spin worker [--no-dream]` | The `Stop`, `PreCompact` and `SessionEnd` hooks, to drain the extraction queue |
| `spin select --session S --id ID --agent A` | The `UserPromptSubmit` hook, to run the recall selector |

The MCP tools match the CLI: `memory_read`, `memory_write`, `memory_delete`, `memory_search`, `memory_index`,
`memory_status`, `memory_projects`, `memory_project`, `memory_dream` (starts a forced dream in the background),
`memory_log` and `memory_history`.

## Settings

| Variable | Default | What it does |
|---|---|---|
| `SPINDLE_HOME` | `~/.spindle` | Where everything is kept. Must be absolute. |
| `SPINDLE_PROJECT` | (folder's link) | Pin the session's project by name. |
| `SPINDLE_BACKEND` | (none) | `claude` or `codex`: use that CLI for every model call, with no fallback. |
| `SPINDLE_MODEL_SELECT`, `_EXTRACT`, `_DREAM` | see below | The model for a role, on the first CLI tried. |
| `SPINDLE_RECALL_WAIT` | `4` | Seconds the prompt hook waits for the selector before leaving the picks to the next tool call. |
| `SPINDLE_RECALL` | (model) | `local` picks by word overlap in the hook itself, with no model call. |
| `SPINDLE_INNER` | (unset) | Set on every process spindle starts; hooks that see it do nothing. Don't set it yourself. |

Default models: the selector uses the session's own CLI (Claude: `sonnet`; Codex: `gpt-6-luna`, low effort).
Extraction uses Codex `gpt-6-luna` at low effort, then Claude `sonnet` if that fails. Dreaming uses Codex
`gpt-6-luna` at medium effort, then Claude `opus`.

## Files

Everything is plain files under `~/.spindle`. You can read and edit them by hand. Run `spin index` afterwards to
check the result; the index is rebuilt on the next change.

```
global/                     the store loaded in every project
projects/<name>/            one project's store
  project.json              {"name", "description", "folders": [...], "created"}
  memory/<name>.md          one memory
  memory/MEMORY.md          the index, rebuilt by code after every change; don't edit
  changes.jsonl             every write and delete, with the text before and after
  state.json                dream gate: last dream, sessions since
  dreams.jsonl              each dream's summary
  lock, dream.lock          flocks
events.jsonl                activity log, all projects
cursors.json                how far extraction has read each transcript
recall/<session>.json       what a session was shown, and its pending pick
queue/                      extraction jobs waiting for the worker
hooks.log                   hook errors (hooks never fail a session)
worker.log                  extraction, dreams the worker ran, model fallbacks
dream.log                   the schedule's runs
```

A memory file uses Claude Code's format:

```markdown
---
name: no-attribution
description: Never add Claude attribution to commits or PRs, in any repo
metadata:
  type: directive
---

The rule.

**Why:** the reason.
**How to apply:** when it applies.
```

The four types are `user`, `directive`, `project` and `reference`. Names are kebab-case and are also the file name.

## Troubleshooting

Start with `spin log -f` in a terminal while you work. It shows each session start, each recall pick and how long
it took, each save and tool call, and each extraction or dream. It also shows anything skipped, with the reason.

| Symptom | Check |
|---|---|
| Nothing in `spin log` for a session | The hooks aren't running. Look at `hooks.log`. In Codex, check that the hooks are trusted (`/hooks` in the CLI). Cloud sessions such as claude.ai project threads never run your local hooks. |
| Recall never picks anything | `spin log --kind select` shows the picks and errors. A one-word prompt, no memories, or the session's 61,440-byte recall budget all skip recall, and `skip` events say which. |
| Picks show up a turn late | The selector took longer than `SPINDLE_RECALL_WAIT`, so the picks were attached after the next tool call. This is expected. A turn with no tool call drops them, and a `skip` event says so. |
| Nothing gets saved after a session | `spin log --kind extract` and `--kind skip`. Extraction skips turns where the user wrote under 20 characters and turns where the agent already saved memory itself. |
| A save landed in the wrong project | `spin status` shows which project this folder hints at. Link the folder with `spin project NAME --link .`, or name the project in the write. |
| A memory got worse | `spin history NAME --full`, then `--restore N`. |
| Codex isn't installed, or fails | Extraction and dreams fall back to Claude; `worker.log` records each fallback. `SPINDLE_BACKEND=claude` skips Codex altogether. |
| Dreams never run | `spin status` shows the gate: 24 hours and 5 sessions since the last dream. Only sessions extraction has read count. `dream.log` shows the schedule's runs. |
