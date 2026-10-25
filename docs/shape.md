# The shape of spindle

What spindle is and isn't, as rules a change can be checked against. If a change breaks one of these, either the
change is wrong, or this doc changes in the same PR, with the reason added to [design.md](design.md).

## Invariants

### Memory

1. **One file per subject**, in Claude Code's format: frontmatter `name`, `description`, `metadata.type`, then the
   body. The name is kebab-case and is also the file name.
2. **Exactly four types:** `user`, `directive`, `project`, `reference`.
3. **Files are the truth.** `MEMORY.md` is always rebuilt by code from the files and is never hand-written. Logs and
   state can be deleted without losing a memory.
4. **Changes overwrite in place, and wrong memories are deleted.** There are no versions, "superseded" flags or
   history in context. Every write and delete is appended to `changes.jsonl` with the old text.
5. **Durable facts only.** No transcripts, activity summaries, code structure, git history or anything already in
   CLAUDE.md or AGENTS.md.

### Projects

6. **Every memory belongs to exactly one store:** `global`, or one named project.
7. **Everything is visible everywhere.** Recall, search and extraction read every store. No path filters memory by
   project.
8. **Outside its store, a memory is always named `<store>/<name>`**: in indexes, recall, tool results, the CLI and
   logs.
9. **A folder only hints.** The hint picks the starting index and where an unnamed write goes. Nothing is stored as
   "the current project", and nothing has to be switched.
10. **Only an explicit act links a folder**: `memory_project` or `spin project --link`. Writes, extraction and
    dreams never do.
11. **Projects are named by meaning**, not derived from paths or repositories.

### Context budget

12. **Session start fits in 9,500 characters**: instructions, the hinted project's index, the global index, and one
    line per other project. Other projects' indexes are never loaded whole.
13. **Recall picks at most 5 files per message, each cut at 4 KB, and at most 61,440 bytes per session** until a
    compaction or clear.
14. **The index is capped at 200 lines and 25 KB.** The oldest lines drop first.

### Runtime

15. **Hooks never block on slow work.** The only wait is the prompt hook's bounded wait for recall (4 s by default).
    Extraction, selection and dreaming run in detached processes.
16. **Hooks never fail a session.** Errors go to `hooks.log`, and the hook exits 0.
17. **No recursion.** Every process spindle starts carries `SPINDLE_INNER=1`, and hooks that see it do nothing.
18. **One writer per store at a time.** Writes take the store's lock and never hold two store locks at once.
    At most one worker runs, and at most one dream per store.
19. **Side models run through the user's signed-in `claude` and `codex` CLIs**, with tools, MCP, settings and saved
    sessions turned off. Spindle has no API keys of its own.
20. **Background passes (extraction, dreams) try Codex first and fall back to Claude.** Selection uses the
    session's own CLI.

### Interfaces

21. **One core.** The MCP tools and the CLI are thin wrappers over `memory.Client`, at parity and with the same
    output text. A new capability goes into the client first and then into both interfaces. `extract` and the
    internal commands are CLI-only.
22. **The selector's system prompt is Claude Code's recall prompt, word for word.** Session context goes in the
    message, not the system prompt.
23. **The agent writes through MCP, never by editing files directly**, so the same instructions work in Codex.

### People

24. **Everything a person might want to check is a plain file** under `~/.spindle`, and `spin log` shows what
    memory did and why.
25. **Old versions reach an agent only through `memory_history`**, which labels them as superseded.

## Non-goals

- **Not a transcript store or search engine over sessions.** The old episode-based spindle (`v0-episodes`) was
  that; this one is not.
- **Not per-repository memory.** Repositories are folders that hint at a project, nothing more.
- **Not a replacement for CLAUDE.md or AGENTS.md.** Instruction files are maintained by people and win when a
  memory conflicts with them. Dreams reconcile memory against them.
- **Not a task tracker or plan store.** In-progress work belongs in plans or tasks.
- **No embeddings or vector search** while one-line descriptions and a model selector do the job.
- **No cloud service, accounts or sync** in this version. It runs on one machine.
- **No hidden state.** There is no database or binary format.

## Checking for drift

Before merging a change, check that:

- [ ] Start context and recall stay within the budgets above. `TestStartContext` and the recall tests guard this.
- [ ] Nothing reads from only one project where every project should be visible.
- [ ] Every new name shown to an agent or a person is a `<store>/<name>` ref.
- [ ] Nothing new links folders, creates projects behind the user's back, or keeps a "current project".
- [ ] Any new capability exists in `memory.Client`, `spin` and the MCP tools, or the reason it doesn't is written down.
- [ ] The hooks still never wait on a model, apart from the bounded recall wait.
- [ ] Prompts copied from Claude Code are still copied; any change is noted in `prompts.go`.
- [ ] `go vet ./...` and `go test ./...` pass.
- [ ] architecture.md, usage.md and this doc still describe what the code does.
