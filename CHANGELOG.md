# Changelog

## Layered memory

Spindle is rebuilt around an append-only log. See [design.md](docs/design.md) for why.

- **Four layers per store.** A log of one-line gists, each pointing at the transcript line, file or note it came
  from; rollups by session, day and week; rule topics; and wiki pages, one per entity. v1's typed memory files,
  `MEMORY.md` indexes and in-place rewrites are gone.
- **Ingest replaces extraction.** After each turn a side model writes zero or more gists with a scope, project or
  global. It never rewrites anything.
- **Dream is an agent session** (Codex, then Claude) over the entries since its checkpoint. It reads pages,
  transcripts, the project's repo and every store, and edits through `spin dream write` and `spin dream retire`,
  which check each edit and record it with its reason and evidence in `page-changes.jsonl`. Pages changed any
  other way are recorded too. `spin dream --dry-run` runs the session with nothing written.
- **Per-prompt recall is gone.** Sessions start with a 12,000-byte index: instructions, rules, a timeline whose
  detail fades with age, wiki page names and other projects. `UserPromptSubmit` and `PostToolUse` hooks are no
  longer installed; old installs that still send them do nothing.
- **Unlinked folders** put their project notes in `~/.spindle/unfiled`; linking the folder adopts them.
- **New commands:** `spin log` (now the memory log; `append` adds an entry), `spin zoom`, `spin rules`,
  `spin wiki`, `spin rollup`, `spin ingest`, `spin migrate`, `spin activity` (the old `spin log`).
  Removed: `status`, `show`, `write`, `rm`, `history`, `extract`, `select`.
- **MCP tools:** `memory_append`, `memory_search`, `memory_zoom`, `wiki_read`, `rules_list`, `rollup_read`,
  `memory_projects`, `memory_project`. The write, delete, status, source, dream, log and history tools are gone;
  agents only append.
- **Model roles** are `ingest`, `rollup` and `dream` (`SPINDLE_MODEL_INGEST`, `_ROLLUP`, `_DREAM`), all Codex
  first with Claude as fallback. `SPINDLE_RECALL` and `SPINDLE_RECALL_WAIT` are gone.
- **Activity** goes to `activity.jsonl`; v1's `events.jsonl` is left as it was.
- **Migration:** run `spin migrate`. It writes the layers beside v1's files and never changes them, and a rerun
  adds only what is missing. The repo-root store migration from before projects were named is removed.
- The OpenCode plugin sends only SessionStart, Stop and PreCompact, and puts the index in the system prompt.

## Viewer

- New: `spin tui`, a read-only terminal viewer of every project's memories, with each memory's body, history and
  activity, and search. It adds Bubble Tea and Lip Gloss as dependencies.

## OpenCode

- `spin install opencode` prints a plugin for OpenCode 2 to save as `~/.config/opencode/plugins/spindle.js`, with
  the `mcp` entry for `opencode.json` in its header. It sends OpenCode's session hooks to `spin hook --agent
  opencode`: the start context goes in the system prompt, and picks go after the message they came with.
- The plugin appends each session's messages to `~/.spindle/opencode/<session>.jsonl`, which extraction reads.
  Subagent sessions are skipped.
- An `opencode` source sweeps that folder (`$SPINDLE_HOME/opencode` if set, or `--dir`), so the worker and the
  hourly `spin dream --due` find OpenCode sessions the plugin wrote but no hook reported.
- `scripts/check-opencode-api.sh [VERSION]` checks that OpenCode's published plugin API still has what the plugin
  uses.

## Sources

- Sessions come from sources: `claude-code`, `codex` and `opencode`, listed in `~/.spindle/sources.json` (all three
  when the file is missing). With no `sources.json`, an existing install picks up `opencode` too, and its first
  sweep reads none of the sessions already in the folder. With a `sources.json`, add it with `spin sources add
  opencode`. Extraction and dreams read sessions through them.
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
