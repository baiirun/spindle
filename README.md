# spindle

Long-term project memory for Claude Code and Codex, through their hooks and an MCP server. Memories are typed
Markdown files (user, directive, project, reference) with a MEMORY.md index, written by a background extraction
pass after each turn, recalled per message by a side-model selector, and consolidated daily. See
[docs/design.md](docs/design.md).

    go build -o spin ./cmd/spin
    ./spin install claude   # or codex: prints the hook and MCP config to merge
    ./spin status           # what the current project remembers
    ./spin log -f           # follow recalls, saves and memory tool calls as they happen
    ./spin search vet       # also: show NAME, write NAME < body.md, rm NAME, index

Status: prototype. Unit-tested, and checked once end to end with `claude -p`.
