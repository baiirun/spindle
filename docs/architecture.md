# How spindle works

This is the mechanism: what runs, when, what it reads and writes. [design.md](design.md) covers why, and
[shape.md](shape.md) lists what must stay true.

## Pieces

```
Claude Code / Codex session
  │ hook events (JSON on stdin)         │ MCP tool calls (stdio)
  ▼                                     ▼
spin hook ──────────────┐        spin mcp
  │  start, recall,     │          │
  │  queue extraction   │          │
  ▼                     ▼          ▼
internal/hooks     internal/memory.Client  ◄── spin show/write/search/... (CLI)
        │                │
        └──────► internal/memory ──► ~/.spindle (plain files)
                         │
                 spin select / spin worker / spin dream --due   (detached processes, launchd)
                         │
                 internal/model ──► claude -p  |  codex exec
                 internal/source ──► internal/transcript (finds and reads sessions)
```

| Package | Job |
|---|---|
| `cmd/spin` | The one binary: hooks, MCP server, CLI, worker, selector, dream runner. |
| `internal/hooks` | Turns one hook event into memory actions and the context to add. Prints install config. |
| `internal/mcp` | A minimal stdio MCP server, and the memory tools as thin wrappers over `memory.Client`. |
| `internal/memory` | Stores, projects, refs, recall, extraction, dreaming, the queue and worker, logs, prompts. |
| `internal/model` | Side-model calls through the `claude` and `codex` CLIs, and the Codex-then-Claude fallback. |
| `internal/source` | The places sessions come from: lists the sessions each one has that changed, and reads one session. |
| `internal/transcript` | Reads Claude Code and Codex transcripts into one list of user and assistant messages, leaving out tool calls and injected text. |

Hooks never wait on a model except the prompt hook's short wait for recall. Anything slow runs in a detached
`spin` process that carries `SPINDLE_INNER=1`, so the hooks fired by its own `claude` or `codex` calls do nothing.

## Stores, projects and refs

A **store** is a folder of memories: `~/.spindle/global` or `~/.spindle/projects/<name>`. A memory is one Markdown
file there. Each store has a `MEMORY.md` index that code rebuilds after every change, grouped by type, newest first
within a type, and capped at 200 lines and 25 KB.

Everywhere outside a store, a memory is named by its **ref**, `<store>/<name>`. Reading or writing a bare name tries
the session's project, then global, then any project that has it, so writing an existing name updates it where it
is.

A session's **context** is its folder, and the project that folder hints at. `memory.Hint` checks
`SPINDLE_PROJECT` first, then the project with the deepest linked folder that contains the working directory.
Without either, the session has no project. Hooks and the client work out the context again on every call. It
decides two things: what the session starts with, and where a write goes by default. It doesn't limit what can be
read.

**Write routing** (`route`), first match wins:

1. a project in the name (`eldspire/x`) or in the op's `project` field (created if new);
2. the session's project, if it already has that name;
3. global, if it already has that name;
4. global, for type `user`;
5. the session's project;
6. otherwise an error: this folder has no project, so name one.

Deletes go wherever the name is found. A write never links a folder. Only `memory_project` and
`spin project --link` do that.

`ApplyAll` groups ops by store and applies each group under that store's lock. It never holds two store locks at
once. Every write and delete is appended to the store's `changes.jsonl` with the text before and after, and then the
index is rebuilt.

## Session start

`SessionStart` (startup, resume, clear, compact) returns:

1. the memory instructions (`Instructions` in `prompts.go`): the types, where memories go, and when to read and write;
2. the global index;
3. the session's project's index, with refs, or a note that the folder has no project;
4. one line per other project: name, memory count, description, most recently changed first.

The whole thing fits in 9,500 characters. What the instructions leave is shared between the sections: 20% for
global, 65% for the session's project and 15% for the other projects. A section that needs less than its share
passes the rest on, first to the sections after it and then to any that still has lines to show. Each section is
cut on its own, so a long project index never pushes global out. A cut index keeps its directives, then its most
recently changed memories, and ends with a line such as `+34 more in spindle, use memory_search`. Everything cut is
still found by recall and search. `memory_index` and `spin index` show every section uncut.

The global dream is told global's share and how much of it global uses, so it keeps global small enough to load
whole.

On `clear` and `compact` the session's recall state is reset, so those memories can be recalled again.

## Recall

On each `UserPromptSubmit`:

1. Skip if the prompt is one word, there are no memories at all, or the session has already been shown 61,440 bytes
   since its last compaction. Each skip is logged.
2. Record a pending selection in `recall/<session>.json` with the prompt, the session's project, its folder and its
   transcript path.
3. Spawn `spin select`, and wait up to `SPINDLE_RECALL_WAIT` (4 s).
4. `spin select` gives the selector:
   - the newest 200 memories of every store as `[type] project/file (mtime): description`;
   - the files already shown this session;
   - the session context: the folder, the project and its description, the projects already recalled from, and
     the user's last three messages, read from the transcript.

   The system prompt asks for only the memories that will clearly help, and is wary of profile and overview memories that merely share words with the message. It returns up to 5 files. A pick without its
   project prefix is accepted when only one store has that file.
5. If the picks are ready inside the wait, they go in with the prompt. If not, `PostToolUse` attaches them after the
   next tool call. `Stop` drops picks that never arrived.

An attachment starts with "Retrieved for possible relevance". Each file is cut at 4 KB. A file more than a day old
carries a "This memory is N days old" warning, so the agent checks it against what it sees now. A file is shown at most once per session.
`SPINDLE_RECALL=local` swaps the model for word-overlap matching in the hook itself.

## Sources

A source is one place sessions live. It does two things: list its sessions that changed between two times, and
read one session as user and assistant messages. Each message's `Line` is its position in the session and only
grows, so a cursor is a number for every source. A session is known by its location, which for the local sources
is the transcript's path.

| Kind | Lists | Leaves out |
|---|---|---|
| `claude-code` | `*.jsonl` under `~/.claude/projects`, by modified time | `subagents/` folders |
| `codex` | `*.jsonl` under `~/.codex/sessions`, by modified time | |

`sources.json` lists the sources; without it, both are read.

**The sweep.** Each worker run starts with one. For each source it lists the sessions that changed after the
source's mark (in `sweeps.json`) and up to 10 minutes ago, so a session still going is left to its hooks. A
session already read after its last change is passed over. The rest are read, and those with messages past their
cursor are queued as jobs. The mark then moves up. A new source's first sweep only sets its mark. A source that
fails keeps its mark, so the next sweep tries the same window, and the others go on.

## Extraction

Jobs come from two places. `Stop`, `PreCompact` and `SessionEnd` write a job (agent, session, transcript, folder,
the project at that moment) to `queue/` and start `spin worker`, so a local turn is read within seconds. The sweep
writes jobs for the sessions no hook reported, with the folder the transcript records. Only one worker runs at a
time (`worker.lock`). A worker that finds the lock held exits, and the running one picks up the new job, because
it reads the queue until it is empty. Several jobs for one transcript make one pass.

A pass reads the session through its source, and takes the messages after its cursor in `cursors.json`. It skips when:

- there are no new messages;
- the new user text is under 20 characters;
- the transcript is a subagent's;
- the main agent wrote memory itself during those messages (only the agent's own writes count, not the CLI's).

Otherwise it makes two model calls, so the model reads what it might change before it writes:

1. **Read.** Input: the session section (folder, project, all project names), every store's file list and the new
   messages (each message cut at 6,000 characters, the whole window at 60,000). Output: `nothing_to_save`, or which
   files to read (at most 20).
2. **Write.** Input: those files in full, the list again and the messages. Output: `write` and `delete` ops, each
   with a `project` (or "" for the default). Code routes and applies them.

The cursor then moves to the last message read, even if the pass was skipped. A failed model call leaves it where
it was, so the next turn retries. The session is counted toward the dream gate of its project and of every store
the pass changed.

## Dreaming

A dream is one pass over one store. Its phases are orient, gather recent signal, consolidate, prune, and
reconcile against the instruction files. The model is given:

- every memory file in the store;
- `CLAUDE.md`, `AGENTS.md` and `.claude/CLAUDE.md` from the project's first linked folder;
- recent messages from the sessions counted since the last dream, read through their sources, newest sessions
  first, up to 120,000 characters.

It returns write and delete ops plus a summary. Code applies the ops to that store only, resets the gate, and
appends the summary to `dreams.jsonl`.

**The gate:** a store is due when at least 5 sessions have counted toward it and its last dream is at least
24 hours old (or it has never dreamed).

**When it's checked:**

- hourly, by the launchd job (`spin dream --due`), after a worker run that sweeps the sources;
- at the end of every worker run.

**Running one by hand:** `spin dream --force`, or `memory_dream`, which starts `spin dream --force` in the
background.

Each store has a non-blocking `dream.lock`, so a second dream on the same store exits at once.

## Models

All side calls go through a CLI the user has already signed in to:

- `claude -p` with no tools, no MCP servers, no settings and no saved session;
- `codex exec`, read-only and ephemeral, with an output schema.

| Role | First | Fallback |
|---|---|---|
| select | the session's own CLI (`sonnet`, or `gpt-6-luna` at low effort) | none |
| extract | Codex `gpt-6-luna`, low effort | Claude `sonnet` |
| dream | Codex `gpt-6-luna`, medium effort | Claude `opus` |

The fallback (`model.Fallback`) moves to the next model on any error, and also when a reply that should be JSON
isn't. `SPINDLE_BACKEND` pins one CLI for every role, with no fallback.

## Logs

- `events.jsonl` at the home records what memory did, for people reading along. Each line names the session's
  project. Kinds are start, select, recall, skip, call (an MCP or CLI call, with its caller), extract, sweep and dream.
  Nothing reads it back into context.
- `changes.jsonl` in each store is the audit trail behind `history` and `--restore`.

## Concurrency

| Lock | Held by | Kind |
|---|---|---|
| `<store>/lock` | anything changing a store's memories, `project.json` or `state.json` | waits |
| `<store>/dream.lock` | a running dream | non-blocking |
| `worker.lock` | the running worker, and with it every sweep and `sweeps.json` | non-blocking |
| `cursors.lock` | updates to `cursors.json` | waits |
| `recall/<session>.json.lock` | updates to one session's recall state | waits |
| `migrate.lock` | upgrading stores written by an earlier version ([CHANGELOG](../CHANGELOG.md)) | waits |

Every lock is an `flock`, so the kernel releases it if a process dies. Files are written to a temporary file and
then renamed into place.
