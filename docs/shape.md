# The shape of spindle

These are the rules that define spindle. A change is checked against them before it merges. If a change needs to
break one, it updates this doc in the same PR and adds its reason to [design.md](design.md).

## Rules

### Layers

1. **The log is the source of truth.** Each store's `log.jsonl` only grows: `Append` adds whole batches and
   nothing rewrites a line. A torn final line from a crash is ignored; any other damage is reported, not skipped.
2. **A log entry is one gist of at most 280 bytes, with a pointer** to the transcript line, file or note it came
   from. Gists are checked once, by `ParseGist`, and never clipped. Migrating v1 is the one writer that clips, and
   its pointer leads to the full text.
3. **Rollups are a cache.** Each is one line over a session, day or week, written by a side model and fresh only
   while its digest matches what is under it. Deleting any rollup costs a model call, never a fact.
4. **Rules and wiki pages are authoritative too,** because people edit them. Dream edits them and never
   regenerates them from the log.
5. **Every rule and page change goes through `ApplyEdits`,** which records it in `page-changes.jsonl` with the
   text it replaced, its evidence and its reason. An edit that changes nothing records nothing.
6. **A page cites the log entries it rests on,** in its own store. Dream may cite only entries its session was
   given or the page already cites.
7. **Rule topics group directives.** One file per topic, not per directive. Dream's prompt asks for this; nothing
   counts files.

### Stores and projects

8. **Every entry and page lives in exactly one store:** `global`, `unfiled` or a named project.
9. **Every store is readable everywhere.** Search and zoom read all of them; a folder's project decides only what a
   session starts with and where new entries go.
10. **A folder with no project files its project notes in `unfiled`,** tagged with the folder. Linking the folder
    adopts them into the project, once; unfiled itself never changes.
11. **Folders are linked to projects on purpose,** with `memory_project` or `spin project --link`.

### Context budget

12. **Session start fits in 12,000 bytes, for any input.** It holds the instructions, the rules (4,000 bytes in
    full, then by name), the session's project's timeline and global's (70/30, unused share passed on), the wiki's
    page names and a line per other project.
13. **The timeline's detail fades with age.** It starts from weeks and opens the newest node it can afford, so the
    newest entries show word for word. A node with no fresh rollup shows as its parts. What doesn't fit is dropped
    oldest first, with a line saying how many entries.
14. **Every timeline line starts with an id that `memory_zoom` opens,** down to the entry and its pointer.

### Runtime

15. **Hooks return quickly and never wait on a model.** Ingest, rollups and dreams run in a detached worker.
16. **Hooks always let the session continue.** Errors go to `hooks.log`, and the hook exits 0. A store that can't
    be read still gives an index.
17. **Spindle's own model calls run with `SPINDLE_INNER=1`,** and hooks that see it return at once.
18. **One worker runs at a time,** and it alone writes rollups and moves sweep marks. Each store has one writer at
    a time (`lock`) and one dream at a time (`dream.lock`).
19. **No message is read twice.** Ingest starts after the later of its cursor and the last line this session's
    entries cite, so a crash between appending and moving the cursor doesn't duplicate notes.
20. **A dream is one agent session per store,** and it changes pages only through `spin dream write` and
    `spin dream retire`, which work only while that store's dream runs. Pages changed any other way are recorded
    before and after the session. The checkpoint moves only after a session that finished, and only past the
    entries it read.
21. **Side models run through the user's signed-in `claude` and `codex` CLIs,** Codex first, Claude on failure.
    Tools, MCP servers, settings and saved sessions are off for these calls.

### Interfaces

22. **`memory.Client` implements every operation the CLI and the MCP tools share,** and both print its text.
23. **Working agents only append.** No MCP tool changes rules or the wiki; those change through a dream session or
    a person.
24. **Subagents never write memory.**
25. **Every piece of state is a plain file under `~/.spindle`,** and `spin activity` shows what spindle did.

### Sessions

26. **Spindle learns from the sessions its sources list,** and reads them only through sources. A new source
    starts from when it was added, not from its history.

## Scope

Spindle's job is long-term memory for one person's agents on one machine. Neighbouring jobs belong to other tools:

- **Instruction files** (CLAUDE.md, AGENTS.md) are maintained by people. Dream reads them and leaves out what they
  already say.
- **Plans and task lists** hold the work in progress.
- **Transcripts** stay with the agent that wrote them. Spindle keeps cursors and pointers into them.
- **Sharing** between people, Slack and meeting sources, and redaction are out of scope for now.

## Before merging

- [ ] The index stays within 12,000 bytes. `TestRenderIndexCap` and `TestHookSpeed` check this.
- [ ] Nothing but `Append` writes a log, and nothing but `ApplyEdits` writes a page.
- [ ] Reruns converge: ingest, dream, migration and adoption tests cover a rerun after a crash.
- [ ] Every read that should see all stores sees all of them.
- [ ] Each new capability is in `memory.Client`, `spin` and the MCP tools, or the doc says why it's CLI-only.
- [ ] `go vet ./...` and `go test ./...` pass.
- [ ] architecture.md, usage.md and this doc match the code.
