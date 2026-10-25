# Changelog

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
