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

This prints a `hooks` block (SessionStart, Stop, PreCompact, SessionEnd) to merge into `~/.claude/settings.json` by hand, plus the command that adds the MCP
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

### OpenCode

```sh
spin install opencode > ~/.config/opencode/plugins/spindle.js
```

OpenCode has no command hooks, so this prints a plugin for OpenCode 2's plugin API instead (1.x plugins use another
API and won't load it). It passes OpenCode's session hooks to `spin hook --agent opencode` and puts the index in the
system prompt of each model request. OpenCode keeps sessions in its database, so the plugin also appends each
session's messages to `~/.spindle/opencode/` for ingest, and the `opencode` source sweeps that folder for sessions the plugin wrote but no hook reported.
Subagent sessions are skipped. The comment at the top of the file has the `mcp` entry to merge into
`~/.config/opencode/opencode.json`. Stop rides on OpenCode's `experimental.idle` hook, which may change between
releases; `scripts/check-opencode-api.sh` checks the plugin against the latest published API.

### Dream schedule (macOS)

```sh
spin install launchd > ~/Library/LaunchAgents/com.spindle.dream.plist
plutil -lint ~/Library/LaunchAgents/com.spindle.dream.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.spindle.dream.plist
```

This runs `spin dream --due` every hour and once when it loads. The plist's `PATH` covers `/opt/homebrew/bin`,
`/usr/local/bin`, `/usr/bin` and `/bin`. If `which codex claude` shows them somewhere else, add that folder to it.
Output goes to `~/.spindle/dream.log`. Without the schedule, dreams still run after the worker's runs once they are due.

### Link your folders

```sh
cd ~/code/spindle && spin project spindle --description "Portable agent memory" --link .
```

`--link .` links the current folder's repository root. A folder only hints at which project a session is in;
every project stays searchable everywhere. Linking adopts what was noted in the folder while it had no project.
Agents can do the same with `memory_project`.

### Migrate from v1

```sh
spin migrate
```

This turns each store's v1 memories into log entries, rule topics and wiki pages next to the v1 files, which it
only reads. Run it again any time: it adds only what is missing. Keep the old binary's hooks off once the new
ones are in, or v1 keeps writing memories the new one only sees on the next `spin migrate`.

### Undo

- Remove the `spin hook` entries from `~/.claude/settings.json` and `~/.codex/hooks.json`, and delete
  `~/.config/opencode/plugins/spindle.js`.
- `claude mcp remove --scope user spindle`, delete `[mcp_servers.spindle]` from `~/.codex/config.toml` and the
  `spindle` entry under `mcp` in `~/.config/opencode/opencode.json`.
- `launchctl bootout gui/$(id -u)/com.spindle.dream`, then delete the plist.
- Memory stays in `~/.spindle` until you delete it. v1's `memory/` folders are never changed, so the old binary
  still reads them.

## Commands

Most commands work out the session's project from the working directory; `--project NAME` overrides it, and
`global` is a valid name. Store-level commands take `--store NAME`, defaulting to this folder's project, or
global when it has none.

| Command | What it does |
|---|---|
| `spin index` | What a session here starts with. |
| `spin log [--store S] [--session ID] [-n N] [-f] [--json]` | Log entries across stores, oldest first. `-f` follows. |
| `spin log append --gist G [--source NOTE] [--global]` | Add an entry by hand. Without a project here it waits in unfiled. |
| `spin search [--layers rules,wiki,rollups,log] [-n N] WORDS...` | Search every store; each hit shows the entries behind it. |
| `spin zoom ID` | Open an index id: `w:2026-W45`, `d:2026-11-08`, `s:SESSION`, `e-...`, `rules/SLUG`, `wiki/SLUG`. Prefix `STORE/` to pick one store. |
| `spin rules [list]` | The rules a session here loads, in full. With `--store S`, one store's topics. |
| `spin rules\|wiki show SLUG` | One page with the entries it rests on. |
| `spin rules\|wiki edit SLUG --reason WHY` | Edit a page, or start a new one, in `$EDITOR`. A page that doesn't parse reopens the editor. |
| `spin rules\|wiki retire SLUG --reason WHY` | Remove a page; its text stays in the history. |
| `spin rules\|wiki history SLUG [--full]` | Every change, newest first, with who, why and the evidence. `--full` adds the text each replaced. |
| `spin wiki [list]` | The store's wiki pages. |
| `spin rollup` | Ingest the queue and write the rollups that are due, without sweeping or dreaming. |
| `spin dream [--dry-run] [--force]` | Run a dream session over this folder's store if its gate is open (`--force`: anyway). `--dry-run` runs the session but has its edits recorded, not written, and prints them, refused ones included. |
| `spin dream --due` | Sweep and ingest, then dream every store whose gate is open. The schedule runs this. |
| `spin ingest SOURCE [--since T]` | Queue every session a source changed since `T` (a time, a date or a duration ago; default 24h) and ingest them. Cursors keep a message from being read twice. |
| `spin ingest FILE.jsonl` | Ingest one transcript now. |
| `spin migrate` | Turn v1 memories into the layers; see Setup. |
| `spin projects` | Every store with its entry, rule and page counts, description and folders. `*` marks this folder's. |
| `spin project NAME [--description D] [--link DIR] [--unlink DIR]` | Create or describe a project, link or unlink a folder. `--link .` is this repository's root. |
| `spin sources` | Where sessions come from and each source's last sweep. `add KIND [--dir DIR]`, `rm KIND` change the list. |
| `spin activity [-f] [-n N] [--session S] [--kind K] [--project P] [--json]` | What spindle did. Kinds: start, call, ingest, rollup, dream, sweep, migrate, warn. |
| `spin tui [--project P]` | A read-only viewer: stores on the left, the store's rules, wiki pages and log entries (newest first) in the middle, the item on the right: `b` its text and evidence, `h` its history, `a` the activity naming it. `tab` or arrows move, `/` searches, `r` rereads, `q` quits. |
| `spin install claude\|codex\|opencode\|launchd [--bin PATH]` | Print config to merge by hand, or the OpenCode plugin. Nothing is written. |

Hooks and background work call these; you don't run them yourself:

| Command | Started by |
|---|---|
| `spin hook --agent claude\|codex\|opencode` | Every hook event, with the event JSON on stdin |
| `spin mcp --agent claude\|codex\|opencode` | The agent, as its MCP server |
| `spin worker [--no-dream]` | The `Stop`, `PreCompact` and `SessionEnd` hooks: sweep, ingest, roll up, dream what's due |
| `spin dream write\|retire --session PATH ...` | A dream session, to change a page; refused outside a running dream |

The MCP tools: `memory_append` (gist, optional scope and source), `memory_search`, `memory_zoom`, `wiki_read`,
`rules_list`, `rollup_read` (session, day or week, at a time), `memory_projects` and `memory_project`. None of them
changes rules or the wiki.

## Sources

Spindle learns from the sessions its sources list. Without a `sources.json` it reads the three local tools:

```json
{"sources": [{"kind": "claude-code"}, {"kind": "codex"}, {"kind": "opencode"}]}
```

| Kind | Reads | Default folder |
|---|---|---|
| `claude-code` | Claude Code transcripts, leaving out `subagents/` folders | `~/.claude/projects` (`$CLAUDE_CONFIG_DIR/projects` if set) |
| `codex` | Codex rollouts | `~/.codex/sessions` (`$CODEX_HOME/sessions` if set) |
| `opencode` | The sessions the OpenCode plugin writes, leaving out subagent sessions | `~/.spindle/opencode` (`$SPINDLE_HOME/opencode` if set) |

`spin sources add KIND [--dir DIR]` and `spin sources rm KIND` change the list; editing the file by hand works too.
Hooks report a session within seconds of each turn. The sweep finds the sessions no hook reported once they have
been quiet for 10 minutes. A source starts from when it was added, so adding one never reads its history; use
`spin ingest SOURCE --since T` for that.

## Settings

| Variable | Default | What it does |
|---|---|---|
| `SPINDLE_HOME` | `~/.spindle` | Where everything is kept. Must be absolute. |
| `SPINDLE_PROJECT` | (folder's link) | Pin the session's project by name. |
| `SPINDLE_BACKEND` | (none) | `claude` or `codex`: use that CLI for every model call, with no fallback. |
| `SPINDLE_MODEL_INGEST`, `_ROLLUP`, `_DREAM` | see [architecture.md](architecture.md#models) | The model for a role, on the first CLI tried. |
| `SPINDLE_INNER` | (unset) | Set on every process spindle starts; hooks that see it do nothing. Don't set it yourself. |

## Files

Everything is plain files under `~/.spindle`. Rules and wiki pages are yours to edit, best through
`spin rules edit` and `spin wiki edit` so the change is recorded; the log is not.

```
global/                     the store loaded in every project
unfiled/                    notes from folders with no project, until one is linked
projects/<name>/            one project's store
  project.json              {"name", "description", "folders": [...], "created"}
  log.jsonl                 the log; only grows
  rollups/s|d|w/<key>.json  session, day and week summaries; safe to delete
  rules/<slug>.md           one rule topic
  wiki/<slug>.md            one entity's page
  page-changes.jsonl        every rule and page change, with before and after
  dream.json                how far dream has read the log
  dream-session.json(l)     a running dream's session; removed when it ends
  lock, dream.lock          flocks
  memory/, changes.jsonl    spindle v1, read by spin migrate and never written
activity.jsonl              what spindle did, all stores
cursors.json                how far ingest has read each session
sources.json                where sessions come from (optional; see Sources)
sweeps.json                 how far each source has been swept
opencode/<session>.jsonl    OpenCode sessions, written by the plugin for ingest
queue/                      ingest jobs waiting for the worker
hooks.log                   hook errors (hooks never fail a session)
worker.log                  the worker's runs, model fallbacks
dream.log                   the schedule's runs
```

A rule topic looks like this:

```markdown
---
name: git-and-prs
title: Git and PRs
evidence: e-19a6e1c2f41a1b2c3d4, e-19a6e1d07c2e9f0a1b2
updated: 2026-11-10T03:00:00Z
---

- No Claude attribution in commits or PRs. Byron's standing rule.
- PR titles start with feat:, fix: or chore:.
```

A wiki page adds `kind:` (person, project, link, tool, concept or org).

## Troubleshooting

Start with `spin activity -f` in a terminal while you work, and `spin log -f` beside it. The first shows each
session start, ingest, rollup and dream, with timings and errors; the second shows the notes as they land.

| Symptom | Check |
|---|---|
| Nothing in `spin activity` for a session | The hooks aren't running. Look at `hooks.log`. In Codex, check that the hooks are trusted (`/hooks` in the CLI). The sweep still finds the session once it's been quiet for 10 minutes; `spin sources` shows the last sweep. |
| A session was ingested but nothing noted | `spin activity --kind ingest`. Turns where the user wrote under 20 characters are skipped, and most turns rightly note nothing. |
| Notes landed in unfiled | This folder isn't linked. `spin project NAME --link .` links it and adopts them. |
| The index shows entries, not summaries | Rollups are written for closed windows, 12 per worker run. `spin rollup` writes the next batch; `spin activity --kind rollup` shows failures. |
| A rule or page got worse | `spin rules history SLUG --full` (or `wiki`), then `spin rules edit SLUG --reason ...`. |
| Dreams never run | The gate is 5 sessions and 24 hours since the last dream. `spin dream --dry-run` shows what one would do now. |
| A dream seems stuck | Sessions have no timeout. `ps` shows the `codex exec` or `claude -p` it runs; stopping it leaves the checkpoint, and pages it already saved stay. |
| "dream.json says ... but the log doesn't match" | The log changed under dream's checkpoint. Move `dream.json` aside to dream from the start. |
| Codex isn't installed, or fails | Calls fall back to Claude; `worker.log` records each fallback. `SPINDLE_BACKEND=claude` skips Codex. |
