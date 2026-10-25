# spindle design

Spindle is a long-term memory that coding agents share. It remembers who the user is, how they like to work, what
each project is about, and where things live. Claude Code and Codex use it through their hooks and an MCP server,
and it is kept as plain files, one memory per file, across all of the user's projects. This doc sets out the
principles it is built on and the reasons for them. [architecture.md](architecture.md) shows how it works, and
[shape.md](shape.md) lists the rules that keep it that way.

## What it is for

An agent starts each session knowing nothing. Within a few turns the user has told it who they are, which
approaches they want and which they've ruled out, what the project is aiming for, and where the dashboards live.
Spindle carries that forward, so the next session, in either tool and in any project, starts from it.

It aims for four things:

- **One memory per person.** It serves every agent and every project they work on, so what was learned anywhere
  is available everywhere.
- **The right few things in context.** A small index loads at the start of each session, and each message brings
  in the handful of memories that bear on it. What reaches context stays the same size however large memory grows.
- **It looks after itself.** Memories are written as work happens and consolidated on a schedule. The user can
  always ask, but never has to.
- **People can see and fix all of it.** Every memory is a file they can read and edit, and every recall, write
  and consolidation shows up in a log.

## Principles

### A memory is a small, typed note with a one-line description

Each memory is one Markdown file about one subject. A frontmatter header gives its name, a one-line description
and its type, and the body follows. The description lets a model judge relevance without reading the body, which
is what makes recall cheap.

There are four types, one for each kind of knowledge an agent needs to carry forward:

| Type | Holds | Example |
|---|---|---|
| `user` | who the user is: role, goals, expertise | "Solo developer, writes Go, runs several projects at once" |
| `directive` | how the user wants work done: rules, corrections, approaches they confirmed | "Commit as baiirun, with no Claude attribution" |
| `project` | the state of the work: goals, decisions, constraints, deadlines | "Extraction runs on Codex first, with Claude as the fallback" |
| `reference` | where information lives and what it's for | "Scheduled dream runs log to ~/.spindle/dream.log" |

Directive and project memories lead with the rule or fact, then give **Why:** and **How to apply:** lines. The
reason lets a later agent handle cases the rule didn't name.

Memory holds what stays true across sessions. Code, git history and instruction files already answer questions
about structure and change. Memory holds what they can't: the person, their preferences, and the reasons behind
decisions.

### An index is always loaded; memories are recalled per message

Each store keeps a `MEMORY.md` index: one line per memory, grouped by type and capped in size. At session start the
agent gets instructions, the index of the project it's in, the global index, and one line for each other project.
That's enough to know what exists.

On each message, a fast side model reads the newest memory descriptions together with the session's context and
picks up to five memories that clearly bear on the message. Their full text is attached to the message.

**Why:** the index orients the agent, and recall brings in detail only when it's needed. Both have fixed caps, so
context cost doesn't grow with memory. Picking by description, with a model, matches on meaning rather than on
shared words.

### Memory is written as work happens

After each turn, a background pass reads the new messages and decides whether anything is worth keeping: a new
fact, a correction to an existing memory, or a memory that turned out wrong. It reads the memories it might
change before it writes. The agent can also write directly when the user asks it to remember or forget
something.

**Why:** people rarely stop to say "remember this". The useful signal is in ordinary conversation: corrections,
confirmed approaches, decisions and their reasons. Running the pass in the background keeps the session fast.

### Memory is consolidated on a schedule

Once a day at most, and only after enough new sessions, a deeper pass ("a dream") goes over one store as a whole.
It merges duplicates, fixes facts that recent sessions have contradicted, prunes memories that have gone stale,
and lines memory up with the project's instruction files. A scheduled job checks every hour whether a dream is
due.

**Why:** writing turn by turn piles up near-duplicates and outdated facts. Consolidating regularly keeps each
memory current and the index sharp. Waiting for enough new sessions means each dream has something to work on.

### Files hold the current truth; history sits beside them

A changed fact rewrites its file, and a wrong memory is deleted. Every change is also recorded with the text it
replaced, and `history` and `restore` read that record.

**Why:** an agent should only ever see what is true now. Older versions in context would mislead it. People
still need to see how a memory changed and to undo a bad change, so the history is kept for them.

### One memory, shared across tools

Recall, extraction and session start run as hooks, and agents read and write memory through MCP tools. Both
Claude Code and Codex support the same hook events and MCP. The background passes run through the user's own
`claude` and `codex` command-line tools.

**Why:** a memory tied to one tool splits the user's knowledge in two. Hooks, MCP and files are common to both
tools. The MCP tools work wherever the agent can run, sandboxes included. Running models through the user's own
CLIs means spindle has no keys, accounts or billing of its own.

### One memory, shared across projects

A project is a named body of work, such as "spindle" or "eldspire", with its own store. Global is a store for
everything that applies everywhere: who the user is, and their standing rules. Every memory is named
`<project>/<name>`, and recall, search and extraction see every store. Folders are linked to projects, and the
folder a session runs in suggests which project it's in. That suggestion sets the index the session starts with
and where new memories go by default.

**Why:** people work across many projects at once, and what's learned in one often matters in another. Naming
projects by what they're about lets one body of work span several folders and repositories. Treating the folder
as a suggestion keeps everything in reach. Linking folders only on purpose means a memory about one project can
be saved from anywhere without moving the session's project.

### Each job runs on the model that suits it

- **Selection** runs on the session's own tool (Sonnet in Claude Code), because the user's message is waiting on it.
- **Extraction and dreams** run on Codex with Luna first, falling back to Claude, because they run in the
  background and need care more than speed.
- **Fallback:** any failed call, or any reply that doesn't parse, moves to the next model.

### One core with several interfaces

One client implements every memory operation. The MCP tools, the command-line interface and the hooks are thin
layers over it, and the tools and commands print the same text.

**Why:** people and agents can each do and see the same things, and the interfaces stay in step because they
share one implementation.

### Everything is observable

An activity log records each session start, recall pick, write, skip, extraction and dream, with timings and
reasons. `spin log -f` follows it live.

**Why:** memory acts on its own. A log of what it did, and why, is what lets people trust it, tune it and fix it.

## Prior art

Spindle's file format, its four-way split of knowledge, the index, the recall selector's prompt, the
extraction pass and dreaming all follow Claude Code's auto memory, which ships at scale and documents its
behavior. Codex's memories informed the use of a small model for background passes. Following an established
design keeps spindle's behavior predictable to anyone who knows those tools.

## Open questions

- **Compaction.** What should a session carry across a compaction? A continuation prompt that includes decisions,
  open work and context would fit beside memory.
- **Ranking.** Should memories that are often recalled and used rank higher when the selector has more candidates
  than it can see?
- **Size.** How should the selector cope past its 200 newest candidates as projects multiply?
- **Reach.** How do cloud sessions, which run no local hooks, read and write memory? A git-backed store is one way.
- **Quality.** How well do recall and extraction work? This needs measuring on real sessions.
