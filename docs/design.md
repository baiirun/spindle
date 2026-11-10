# spindle design

Spindle is a long-term memory that coding agents share. It captures what happens in the user's sessions and
serves it back to agents before and during work. Claude Code, Codex and OpenCode use it through their hooks and an
MCP server, and it is kept as plain files across all of the user's projects. This doc sets out the decisions it is
built on and the reasons for them. [architecture.md](architecture.md) shows how it works, and
[shape.md](shape.md) lists the rules that keep it that way.

## What it is for

An agent starts each session knowing nothing. Within a few turns the user has told it who they are, which
approaches they want and which they've ruled out, what the project is aiming for, and what was tried last week.
Spindle carries that forward, so the next session, in any tool and any project, starts from it.

It aims for four things:

- **One memory per person,** serving every agent and project, so what was learned anywhere is available everywhere.
- **A fixed amount in context.** Each session starts with the same 12,000-byte index however large memory grows,
  and pulls more on demand.
- **It looks after itself.** Notes are taken as work happens, summarized and consolidated in the background.
- **People can see and fix all of it.** Every layer is a file, every claim points at what was said, and every
  change to a rule or page records why.

## Four layers

| Layer | Holds | Written by | Lifecycle |
|---|---|---|---|
| Log | One entry per thing worth keeping: a gist and a pointer to where it was said | ingest, the agent, the CLI | Append-only |
| Rollups | One line per session, day and week | a side model | Derived; rebuilt when stale |
| Rules | Directive topics an agent loads before acting | dream, or the user | Edited and retired with a change record |
| Wiki | One page per entity: person, project, link, tool, concept, org | dream, or the user | As rules |

### The log is the truth

Every other layer can be rebuilt from it, so a better prompt or model can reprocess all of history, and the
pointer lets anyone check a claim against what was actually said. v1 rewrote a memory file in place when a fact
changed; the old text survived only in an audit log nothing read. Here the history is the primary record and the
current truth is derived from it.

### Notes are classified later, by dream

At write time the working agent is mid-task with partial context, and any scheme it applies is frozen into the
data. v1 asked the extraction pass to pick one of four types and a file to rewrite on every turn. Here ingest only
notes what happened, with a scope (this project, or everywhere), and dream sorts notes into rules and pages with the
whole picture in view. The scheme can change and be re-run.

### A side model takes the notes and writes the summaries

An agent summarizing its own session grades its own work and spends its working context doing it. So a cheap side
model reads each turn after it ends and writes zero or more gists, each citing a line; another writes rollups. The
agent can still note something mid-session with `memory_append`, and ingest is shown recent notes so it doesn't
repeat them. OptMem has the working agent write its own summaries inline; that suits one agent with one memory, and
costs the agent attention we would rather it spent on the task.

### Rules and the wiki are separate because they are read differently

Rules are a small set loaded into every session before acting. Wiki pages are pulled on demand when a topic comes
up. A rule file is a topic holding its directives as a list, so rules stay few without a cap: an earlier draft
capped topics at 12, but the real cost is bytes in the index, which already has its own budget, and a count can't
tell a long topic from a short one.

### The wiki is organized by entity

Questions arrive about a thing: a person, a project, a tool. One page per thing gives one place to look and one
place to fix. Rollups answer the other question, what happened when.

### Dream is an agent session, not a completion

Deciding what a note means for the wiki often needs more than the note: the page it might contradict, the
transcript it came from, the code it describes. A single completion has to be handed all of that up front, or
guess. So dream runs as an agent session with tools, like Codex v2's consolidation step: it reads the notes,
opens what it needs (pages, transcripts, the repo, other stores) and edits as it goes.

It edits only through `spin dream write` and `spin dream retire`, each one edit checked and recorded on its own.
A refused edit says why, so the agent fixes it instead of the whole pass failing. The commands work only while
their store's dream runs, so working agents still only append. Codex's sandbox can make the home writable only
as a whole, so the runner also records any page that changed without a command, and the history stays complete.
There is no timeout: a session takes what it takes, and a failed one keeps what it applied.

### Every change carries a reason

Wikis rot without a curator; Librande's One-Page Designs talk (GDC 2010) says "if you are going to use a wiki then
you need a dedicated owner." Dream does that curation, and the reason and evidence on each change show why a claim
changed. When notes disagree, the newer wins, and the reason says what it replaced.

### Anyone can edit any layer above the log

Human-written context helps agents and model-written context can hurt: in one study, human-written AGENTS.md files
raised task success about 4% and LLM-generated ones lowered it about 3% (arXiv 2602.11988). So rules and pages are
plain files the user edits with `spin rules edit` and `spin wiki edit`, recorded like dream's edits, and dream edits
what is there rather than regenerating it.

## Session start

### The index, not per-prompt recall

v1 ran a side-model selector on every message and attached up to five memories. It cost a model call per message,
delayed the prompt by up to four seconds, and often arrived a turn late. Spindle now loads a fixed index at session
start and leaves the rest to the agent: `memory_zoom`, `memory_search` and `wiki_read` when the work calls for it.
Codex v2 memories make the same choice: one summary file under a hard cap at startup, with the detail one read away.

### Detail fades with age

The timeline follows OptMem's `wake`: recent notes word for word, older history as session, day and week lines,
fitted to a fixed budget by opening the newest node it can afford. Every line has an id that opens into its parts,
so nothing is out of reach; it is only further away. Rollups are a cache with a digest of what they cover, so a
missing or stale one shows as its parts, and a side model that is down makes the index longer-grained, never
broken.

### The budget is fixed

Every session pays for the index, so it is 12,000 bytes, not a setting. Rules get the first 4,000; the timelines
share most of the rest, 70% to the session's project and 30% to global.

## Projects

### One memory, shared across projects

Projects are named by meaning ("spindle", "eldspire"), not by repository, and a folder only hints at which one a
session is in. Search and zoom read every store, so what was learned in one project helps in another.

### Unlinked folders keep their notes

A session in a folder linked to no project still produces notes about its work. Dropping them loses them for good,
since the cursor moves on; putting them in global fills every session's index with one folder's details. So they
wait in an `unfiled` store tagged with the folder, and linking the folder adopts them into the project. Unfiled is
never loaded at start or dreamed.

## Running

### Work happens after the turn, in one worker

Hooks never wait on a model. Stop, PreCompact and SessionEnd queue the session, and a detached worker ingests,
writes rollups and dreams what is due. One worker at a time owns the queue, the sweep marks and the rollups, so
none of them needs finer locking.

### Every step converges on rerun

Crashes, retries and two hooks firing at once are normal. Ingest starts after the last line its own entries cite,
so a crash before the cursor moves doesn't duplicate notes. Dream moves its checkpoint last and skips edits already
in place. Migration derives entry ids from what it migrates. Adoption records what it copied.

### Each job runs on the model that suits it

Background passes run Codex's small model first and fall back to Claude, through CLIs the user is already signed in
to, so spindle has no keys of its own.

### One core with several interfaces

`memory.Client` implements what the CLI and the MCP tools share, and both print its text. Agents get only reads
and `memory_append`; rule and page edits are the CLI's and dream's.

## Prior art

- **OptMem** (github.com/VictorTaelin/OptMem): an append-only log of one-line memories, a binary tree of
  summaries, and a `wake` that prints the tree with detail decaying by age within a line budget. Spindle's index,
  its zoomable ids and its rollups-as-cache follow it, over calendar windows instead of power-of-two blocks.
- **Codex memories v2**: per-session summaries from a small model, consolidated by an agent session into one
  capped summary loaded at startup, with the detail read on demand. Spindle takes the startup cap, consolidation as
  a session, the two model sizes, and treating stored text as data, not instructions.
- **Claude Code's auto memory**: spindle v1 followed it: typed files, an index, a recall selector, extraction and
  dreaming. Its instruction files remain the user's, and dream leaves out what they say.

## Open questions

- **Conflicts.** Newer evidence wins today. Should dream keep both claims, dated, when the older one may still
  hold elsewhere?
- **Where rules end and the wiki begins.** Preferences about a tool ("use pnpm") could be either.
- **Usage.** Should pages that agents read often rank higher in search, and unread ones age out, as in Codex?
- **Migrated history.** v1's change rows give a coarse timeline. Re-ingesting old transcripts would give a
  better one, at a model call per turn.
- **Sharing.** Single-user for now. An append-only log and one file per page keep a git-backed store possible.
- **Quality.** How good are the notes, rollups and dreams? This needs measuring on real sessions.
