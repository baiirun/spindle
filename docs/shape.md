# The shape of spindle

These are the rules that define spindle. A change is checked against them before it merges. If a change needs to
break one, it updates this doc in the same PR and adds its reason to [design.md](design.md).

## Rules

### Memory

1. **One subject per file.** Each file has frontmatter `name`, `description` and `metadata.type`, then the body.
   The name is kebab-case and doubles as the file name.
2. **Four types:** `user`, `directive`, `project` and `reference`.
   A file with a missing or other type (say, `feedback` copied from Claude Code) is still a memory: the index
   lists it under Other with its type, and `spin log` shows a warning until it is fixed.
3. **The files are the source of truth.** Code rebuilds `MEMORY.md` from them after every change. Logs and state
   can be rebuilt or thrown away, and no memory is lost.
4. **Files hold the current truth.** A changed fact rewrites its file, and a wrong memory is deleted. Every write
   and delete is also appended to `changes.jsonl`, with the text it replaced.
5. **Memory holds what lasts:** who the user is, how they want work done, the state and reasons of the work, and
   where things live.

### Projects

6. **Every memory lives in exactly one store,** either `global` or a named project.
7. **Every store is visible everywhere.** Recall, search and extraction read all of them.
8. **Outside its store, a memory is named `<store>/<name>`.** That holds in indexes, recall, tool results, the CLI
   and logs.
9. **A session's folder suggests its project.** The suggestion is worked out again on every call. It sets the
   starting index and where an unnamed write goes.
10. **Folders are linked to projects on purpose,** with `memory_project` or `spin project --link`.
11. **Projects are named for what they are about.**

### Context budget

12. **Session start fits in 9,500 characters.** It holds the instructions, the global index, the session's project
    index, and one line per other project. Each section has its own share of the space and is cut within itself,
    so global always loads; a cut index keeps directives and the newest memories and says how many it left out.
13. **Recall attaches at most 5 files per message,** each cut at 4 KB, up to 61,440 bytes per session. The count
    resets at compaction or clear.
14. **Each index is capped at 200 lines and 25 KB,** with the newest lines kept.

### Runtime

15. **Hooks return quickly.** The only wait is the bounded recall wait (4 s by default). Extraction, selection and
    dreams run in detached processes.
16. **Hooks always let the session continue.** Errors go to `hooks.log`, and the hook exits 0.
17. **Spindle's own model calls run with `SPINDLE_INNER=1`,** and hooks that see it return at once.
18. **Each store has one writer at a time.** A write takes that store's lock and holds one store lock at a time. One
    worker runs at a time, and each store has one dream at a time.
19. **Side models run through the user's signed-in `claude` and `codex` CLIs.** Tools, MCP servers, settings and
    saved sessions are switched off for these calls.
20. **Background passes start on Codex and fall back to Claude.** Selection uses the session's own CLI.

### Interfaces

21. **`memory.Client` implements every operation.** The MCP tools and the CLI wrap it and print the same text. A
    new capability is added to the client, then to both interfaces. `extract` and the internal commands are
    CLI-only.
22. **The selector's system prompt stays fixed,** copied as is. Session context goes in the message.
23. **Agents read and write memory through the MCP tools.**

### People

24. **Every piece of state is a plain file under `~/.spindle`,** and `spin log` shows what memory did and why.
25. **Earlier versions reach an agent only through `memory_history`,** which labels them as superseded.

## Scope

Spindle's job is long-term memory for one person's agents: remembering, recalling and consolidating what they
should know across sessions, tools and projects, on one machine. Neighbouring jobs belong to other tools:

- **Instruction files** (CLAUDE.md, AGENTS.md) are maintained by people. They take precedence, and dreams bring
  memory in line with them.
- **Plans and task lists** hold the work in progress.
- **Transcripts** stay with the agent that wrote them. Spindle reads them to extract memories.
- **Recall** works from descriptions and a model selector.
- **State** lives in plain files on the user's machine.

## Before merging

- [ ] Session start and recall stay within the context budget. `TestStartContext` and the recall tests check this.
- [ ] Every read that should see all stores sees all of them.
- [ ] Every name shown to an agent or a person is a `<store>/<name>` ref.
- [ ] Folder links and new projects come only from an explicit request.
- [ ] Each new capability is in `memory.Client`, `spin` and the MCP tools, or the doc says why it's CLI-only.
- [ ] Hooks return within the bounded recall wait.
- [ ] Prompts copied from elsewhere are still as copied, and any change is noted in `prompts.go`.
- [ ] `go vet ./...` and `go test ./...` pass.
- [ ] architecture.md, usage.md and this doc match the code.
