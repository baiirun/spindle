# How spindle works

This is the mechanism: what runs, when, what it reads and writes. [design.md](design.md) covers why, and
[shape.md](shape.md) lists what must stay true.

## Pieces

```
Claude Code / Codex session (OpenCode: through its plugin)
  │ hook events (JSON on stdin)         │ MCP tool calls (stdio)
  ▼                                     ▼
spin hook ─────────────┐         spin mcp
  │ SessionStart: index│           │
  │ Stop etc.: queue   │           │
  ▼                    ▼           ▼
internal/hooks    internal/memory.Client  ◄── spin index/log/search/zoom/rules/wiki/... (CLI)
        │                │
        └──────► internal/memory ──► ~/.spindle (plain files)
                         │
                 spin worker / spin dream --due   (detached processes, launchd)
                         │
                 internal/model  ──► claude -p  |  codex exec
                 internal/source ──► internal/transcript (finds and reads sessions)
```

| Package | Job |
|---|---|
| `cmd/spin` | The one binary: hooks, MCP server, CLI, worker, dream runner. |
| `internal/hooks` | Turns one hook event into the index or a queued job. Prints install config and the OpenCode plugin. |
| `internal/mcp` | A minimal stdio MCP server, and the memory tools as thin wrappers over `memory.Client`. |
| `internal/memory` | Stores and projects, the four layers, ingest, the index, search and zoom, dream, the queue and worker, migration. |
| `internal/model` | Model calls through the `claude` and `codex` CLIs, Codex first and Claude on failure: one-shot completions for ingest and rollups, and agent sessions for dream. |
| `internal/source` | The places sessions come from: lists sessions that changed, and reads one. |
| `internal/transcript` | Reads Claude Code, Codex and OpenCode transcripts into user and assistant messages, each with its line. |
| `internal/tui` | `spin tui`, the read-only viewer. |

## Stores

A store is a folder: `~/.spindle/global`, `~/.spindle/unfiled` or `~/.spindle/projects/<name>`. Each holds:

| File | Layer | Written by |
|---|---|---|
| `log.jsonl` | the log: one entry per line | `Append`, from ingest, `memory_append`, `spin log append`, adoption and migration |
| `rollups/<s\|d\|w>/<key>.json` | rollups | the worker |
| `rules/<slug>.md` | rule topics | `ApplyEdits`, from dream, the CLI's editor and migration |
| `wiki/<slug>.md` | wiki pages | `ApplyEdits`, as rules |
| `page-changes.jsonl` | every page change, with before and after | `ApplyEdits` |
| `dream.json` | how far dream has read the log | dream, and migration once |
| `dream-session.json`, `.jsonl` | a running dream's session and its commands' outcomes | dream; removed when it ends |

A log entry:

```json
{"id":"e-19a6e1c2f41a1b2c3d4","at":"2026-11-10T14:02:00Z","session":"6a1f...","folder":"/Users/b/code/spindle",
 "source":{"kind":"codex_session","path":"/Users/b/.codex/sessions/2026/11/10/rollout-....jsonl","line":412},
 "gist":"Rollups are written by a side model, not the working agent."}
```

A page is Markdown with a small frontmatter that spindle parses strictly: `name` (the slug, matching the file),
`title`, `kind` (wiki only: person, project, link, tool, concept or org), `evidence` (entry ids) and `updated`.

A session's **context** is its folder and the project that folder hints at: `SPINDLE_PROJECT`, else the project
with the deepest linked folder that contains it, else none. It decides what the session starts with and where its
entries go. It never limits what can be read.

## Session start

`SessionStart` (startup, resume, clear, compact) returns the index, at most 12,000 bytes:

1. the instructions: how to note with `memory_append`, how to read with `memory_zoom`, `memory_search` and
   `wiki_read`, and that memory is a claim to check;
2. the rules, the session's project's then global's, in full up to 4,000 bytes and by name after that;
3. without a project, a line saying notes here wait in unfiled, how many there are, and to link the folder;
4. the timelines, the session's project's then global's;
5. the wiki's page names and kinds;
6. one line per other project.

The timelines get what the other sections leave: 70% to the project, 30% to global, and each passes on what it
doesn't use. A store that can't be read whole still shows what it can, and the problem goes to the activity log.

**The timeline** (`Timeline`, `FitTimeline`) is derived from the log and the rollups and never stored. Entries
group into sessions, sessions and loose entries into the day of their first entry, days into ISO weeks, all in
the machine's time zone, placed by time rather than log order. A node shows its rollup's line only while the
rollup is fresh: its `inputs` digest equals the digest of everything under the node, so a new entry, or a child's
new rollup, makes the node and all its parents stale.

Fitting starts from the weeks. A node with no fresh line is always shown as its parts. Then it opens the newest
node with parts into them, again and again, until the newest node's parts don't fit. So the newest entries show
word for word, recent days as sessions, and old history as week lines. If even that is over budget, the oldest
lines go and a first line says how many entries they held.

## Ingest

Jobs come from two places. `Stop`, `PreCompact` and `SessionEnd` write a job (agent, session, transcript, folder,
project) to `queue/` and start `spin worker`. The worker's sweep queues sessions no hook reported, once quiet for
10 minutes (see Sources below). One worker runs at a time (`worker.lock`); one that finds it held exits, and the
running one reads the queue until it is empty. Several jobs for one transcript make one pass.

A pass (`Ingest`) reads the session through its source and starts after the later of its cursor in
`cursors.json` and the last line this session's entries cite in the stores it writes to. It skips subagents, no
new messages, and new user text under 20 characters. Otherwise one model call gets the folder and project, the 30
newest entries of each target store (so it doesn't repeat what the agent noted), and the new messages numbered by
line (each cut at 6,000 characters, the window at 60,000, oldest cut first). It returns gists, each with a line and
a scope:

- `project` gists go to the session's project, or to `unfiled` with the folder when there is none;
- `global` gists go to global.

A gist citing a line that isn't a new message, or that fails `ParseGist`, is dropped and counted in the activity
log. A failed model call or a reply that isn't the schema appends nothing and leaves the cursor, so the next pass
retries. The cursor moves after the entries are appended.

**Adoption.** Linking a folder (`spin project --link`, `memory_project`) copies the unfiled entries from that
folder or below into the project's log, each with `adopted` naming the original. A second link copies nothing.

## Rollups

After draining the queue the worker runs `RollUp` over global and every project. `DueRollups` lists the windows
that are closed (a session quiet for 30 minutes, a day or week whose end has passed), whose parts all have lines,
and that have no fresh rollup. Children come due before parents, so a session, then its day, then its week, over
successive rounds. `Summarize` asks for one line of at most 280 bytes over the parts. At most 12 rollups are
written per run, newest first, so a backlog is worked off over several runs. A failed call leaves the window due.

## Dream

A dream is one agent session over one store, under its `dream.lock`.

**The gate:** a store is due when 5 distinct sessions have added entries since its checkpoint and its last dream
is at least 24 hours old (or it never dreamed). The worker checks every store after each run; the hourly
`spin dream --due` sweeps and ingests first, then checks.

**Before the session**, pages whose files differ from what `page-changes.jsonl` last recorded (hand edits made
outside spin) are recorded as `cli`. Then `LoadDreamInput` gathers up to 300 entries after the checkpoint, oldest
first, the rollups of their sessions and days, every rule topic and page, and the project's linked folders. A page
that doesn't parse stops the dream, since dream would otherwise take it for missing.

**The session** runs in the store's folder (`model.AgentFor(Dream)`: Codex, then Claude; no timeout). Its task
holds the notes with their ids and pointers, the summaries, the rule topics in full, the page list, the folders,
and the commands it may run. It reads pages, the transcripts its notes point at, the project's repo and, through
`spin zoom`, `search`, `log` and `rules`/`wiki list|show`, every store. It changes pages only with:

```sh
spin dream write  --session <path> rules/<slug>|wiki/<slug> --title T [--kind K] --evidence IDS --reason WHY < body.md
spin dream retire --session <path> rules/<slug>|wiki/<slug> --evidence IDS --reason WHY
```

`dream-session.json` in the store names the store, the entries the session may cite and the mode; the commands
refuse to work unless that store's `dream.lock` is held. Each command builds one page from the fields (keeping the
page's evidence), checks it with `CheckEdits` (a reason, text that parses, no retirement of a missing page, evidence
only from the session's entries or the page's own) and applies it with `ApplyEdits` as `dream`. A refused edit
exits non-zero with the reason, so the agent can fix it and try again. Each outcome goes to `dream-session.jsonl`.

| CLI | How it runs | What it may do |
|---|---|---|
| `codex exec` | `--sandbox workspace-write`, cwd the store, `--add-dir` the home, `approval_policy="never"`, ephemeral | read anywhere, write only under the home, no network |
| `claude -p` | `--tools Bash,Read,Grep,Glob`, `--allowedTools` the commands plus Read, Grep and Glob, `--permission-mode dontAsk`, `--add-dir` the home, source folders and project folders, no MCP servers or settings | read those folders, run only the listed commands |

**After the session**, pages that changed without a command (Codex's sandbox can grant the home only as a whole)
are recorded as `dream`, so every change stays in the history. The checkpoint moves only if the session finished,
and never in a dry run; a failed session keeps what it applied, and the next one sees those pages. `spin dream
--dry-run` runs the session with the commands recording what they would write.

## Search and zoom

`LoadCorpus` reads every store, unfiled included. `Search` ranks rules, wiki pages, rollups and entries by how
many query words they contain, then by recency; each hit carries its evidence entries. An unfiled entry a project
has adopted is shown only as the copy. `Zoom` opens a window id into its line and its parts, an entry into its gist
and pointer, or a page ref into the page and the entries it cites. An id several stores have opens in each, and a
`<store>/` prefix picks one.

## Sources

A source lists its sessions that changed between two times and reads one session. A message's `line` only grows,
so a cursor is a number for every source.

| Kind | Lists | Leaves out |
|---|---|---|
| `claude-code` | `*.jsonl` under `~/.claude/projects`, by modified time | `subagents/` folders |
| `codex` | `*.jsonl` under `~/.codex/sessions`, by modified time | |
| `opencode` | `*.jsonl` the plugin writes under `~/.spindle/opencode` | subagent sessions |

`sources.json` lists the sources; without it, all three are read. Each worker run starts with a sweep: for each
source, the sessions that changed after its mark (in `sweeps.json`) and up to 10 minutes ago, with unread messages,
are queued, and the mark moves up. A new source's first sweep only sets its mark. A source that fails keeps its
mark.

## Migration from v1

`spin migrate` reads each store's v1 `memory/*.md` and `changes.jsonl` and writes the layers beside them, never
touching v1's files. Every change row becomes an entry at its time; a memory v1 never logged becomes one too. User
and directive memories become rule topics; project memories wiki pages of kind project, references of kind link,
anything else concept. Each page cites its memory's last logged write. Entry ids derive from what they migrate, so
a rerun appends only what's missing and creates only missing pages. When the log holds only migrated entries,
dream's checkpoint starts after them, since the pages already reflect them.

## Models

All model calls go through a CLI the user has already signed in to, so spindle has no keys of its own. Ingest and
rollups are one-shot completions (`model.Model`): `claude -p` with no tools, MCP servers, settings or saved
session, or `codex exec`, read-only and ephemeral, with an output schema. Dream is an agent session
(`model.Agent`), confined as the table under Dream shows.

| Role | First | Fallback |
|---|---|---|
| ingest | Codex `gpt-6-luna`, low effort | Claude `sonnet` |
| rollup | Codex `gpt-6-luna`, low effort | Claude `sonnet` |
| dream (an agent session) | Codex `gpt-6-luna`, medium effort | Claude `opus` |

`model.Fallback` moves on at any error, and when a reply that should be JSON isn't; `model.AgentFallback` moves
on when a session fails, and the next agent sees what the first applied. `SPINDLE_BACKEND` pins one CLI for every
role.

## Logs

- `activity.jsonl` at the home records what spindle did, for people: start, call (an MCP or CLI call, with its
  caller), ingest, rollup, dream, sweep, migrate and warn. Nothing reads it back into context. v1's `events.jsonl`
  is left where it is.
- `page-changes.jsonl` in each store is the history behind `spin rules history` and `spin wiki history`.

## Concurrency

| Lock | Held by | Kind |
|---|---|---|
| `<store>/lock` | `Append`, `ApplyEdits`, and changes to `project.json` | waits |
| `<store>/dream.lock` | a running dream, for its whole session; its commands check it is held | non-blocking |
| `worker.lock` | the running worker: sweeps, `sweeps.json`, rollups | non-blocking |
| `cursors.lock` | updates to `cursors.json` | waits |
| `sources.lock` | updates to `sources.json` | waits |

Every lock is an `flock`, so the kernel releases it if a process dies. Files other than logs are written to a
temporary file and renamed into place; logs are appended a batch at a time in one write.
