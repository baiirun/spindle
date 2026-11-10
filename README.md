# spindle

One long-term memory for Claude Code, Codex and OpenCode, shared across all of your projects. A side model notes
what happens in each session as one-line gists in an append-only log, each pointing at what was said. Rollups
summarize the log by session, day and week. A periodic dream, an agent session with tools, turns new notes into rule
topics, which every session loads, and wiki pages, one per entity. Each session starts with the rules and a timeline whose detail fades with
age. Everything runs through the tools' hooks and an MCP server, and is kept as plain files in `~/.spindle`.

    go build -o ~/go/bin/spin ./cmd/spin
    spin install claude                # or codex, opencode, launchd: prints config to merge
    spin migrate                       # once, if you used spindle v1: memories become rules, pages and log entries
    spin project spindle --link .      # name this folder's project (it only hints; every project stays visible)
    spin index                         # what a session here starts with
    spin log -f                        # watch notes arrive
    spin tui                           # browse stores: rules, wiki pages and log entries

## Docs

- [Usage](docs/usage.md): setup, commands, settings, files, troubleshooting
- [Architecture](docs/architecture.md): how ingest, rollups, dream and the index work
- [Design](docs/design.md): the decisions and why
- [Shape](docs/shape.md): what must stay true, and what spindle is not
- [Changelog](CHANGELOG.md)

Status: prototype, in daily use on one Mac. Unit-tested, with end-to-end tests that run the binary against stand-in
model CLIs.

MIT licensed.
