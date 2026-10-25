# Changelog

Each PR that changes how spindle behaves adds a line under Unreleased. When a release is cut, Unreleased becomes
its section. CI fails a PR that changes Go code without touching this file, unless the PR has the `no changelog`
label (for refactors, tests and other changes nobody using spindle would notice).

## Unreleased

- Changelog check in CI, and this Unreleased section for new entries.

## CI (#16)

- GitHub Actions on every PR and push to main: tests with `-race` on Linux and macOS, golangci-lint with gofmt
  and `go mod tidy`, and govulncheck (also weekly).
- A folder linked through a symlink is found by its real path too. On macOS, `/var` and `/private/var` no
  longer count as different folders.
- The selector reports a failed read of its recall state instead of treating it as a superseded selection.
- A Codex call fails if its output-schema file can't be written, instead of running with an empty schema.

## Fixes from the test suite (#5, #7 to #15)

The tests in #5 found nine bugs. Each was fixed in its own PR:

- Session start puts global first and gives each section its own share of the space: 20% global, 65% the
  session's project, 15% other projects. A big project no longer pushes global out. A cut section keeps
  directives and the most recently changed memories, and ends with `+N more`. `memory_index` stays uncut. (#15)
- A memory with a missing or unknown type (such as Claude Code's `feedback`) is listed under Other in
  `MEMORY.md`, and each rebuild logs a warning naming those files. (#14)
- `spin history` and `--restore` find a deleted memory by its bare name in any store, global included. (#13)
- A bare-name write updates the memory where it already lives, in any project, instead of making a copy in
  the session's project. (#9)
- The `extract` event in `spin log` says how many characters were cut when new messages run past the
  60k-character window. (#11)
- A dream skips writes aimed at another store and counts them, instead of writing them into its own. (#7)
- Recalled memories are cut at 4 KB of bytes, not 4,096 characters, so non-Latin text stays within the
  cap. (#10)
- Sessions marked while a dream waits on the model are kept for the next dream. (#12)
- The hook exits 0 when `SPINDLE_HOME` is relative, printing the error, so the agent isn't interrupted. (#8)

## Docs (#4, #6)

- `docs/usage.md` (every command, settings, files under `~/.spindle`, troubleshooting),
  `docs/architecture.md` (recall, extraction, dreaming, projects and locking as built), `docs/design.md`
  (what spindle is for and the principles behind it) and `docs/shape.md` (the rules to check changes
  against). MIT license.

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
