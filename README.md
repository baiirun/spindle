# spindle

One long-term memory for Claude Code and Codex, shared across all of your projects. Memories are typed Markdown
files (user, directive, project, reference). A background pass writes them after each turn. A side-model selector
recalls them per message, and they are consolidated daily. Everything runs through the tools' hooks and an MCP
server, and is kept as plain files in `~/.spindle`.

    go build -o ~/go/bin/spin ./cmd/spin
    spin install claude                # or codex, or launchd: prints config to merge
    spin project spindle --link .      # name this folder's project (it only hints; every project stays visible)
    spin log -f                        # watch recalls, saves and tool calls as they happen

## Docs

- [Usage](docs/usage.md): setup, commands, settings, files, troubleshooting
- [Architecture](docs/architecture.md): how recall, extraction, dreaming and projects work
- [Design](docs/design.md): the decisions and why
- [Shape](docs/shape.md): what must stay true, and what spindle is not
- [Changelog](CHANGELOG.md)

Status: prototype, in daily use on one Mac. Unit-tested; recall and extraction checked end to end in both tools.

MIT licensed.
