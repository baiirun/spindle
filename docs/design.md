# spindle design

Spindle gives Claude Code and Codex one long-term memory, shared across all of a user's projects. It is built
from first principles on what the two tools' own memory systems do, and it runs only through seams both tools
share: hooks, an MCP server, and files. This doc records the decisions and why they were made.
[architecture.md](architecture.md) covers how it works, and [shape.md](shape.md) the rules that keep it from
drifting.

## Goals

- **One memory for both tools.** A rule saved in a Codex session is recalled in Claude Code, and the other way round.
- **Work shouldn't feel siloed.** An agent in one project knows about the others and can use what was learned there.
- **Durable facts, not transcripts.** Memory holds who the user is, how they want to work, decisions and where
  things live. It doesn't keep logs of what happened.
- **A fixed budget.** What goes into context per session and per message is capped, however much memory grows.
- **Inspectable.** Plain files, an activity log and a change history that a person can read and fix by hand.

## Decisions

### Follow Claude Code's auto memory

Spindle uses the same model as Claude Code's auto memory:

- one Markdown file per subject, with `name`, `description` and `type` in the frontmatter;
- a `MEMORY.md` index that is always loaded;
- a side-model selector that picks up to 5 files per message from their one-line descriptions, using Claude Code's
  prompt word for word;
- an extraction pass after each turn;
- a daily dream that consolidates.

**Why:** it's a design shipped and tuned at scale, its behavior is documented, and copying it closely keeps
spindle predictable to anyone who knows Claude Code. Where spindle departs from it, the reason is written down
here.

### Four types, with "feedback" called "directive"

`user`, `directive`, `project`, `reference`. **Why:** Claude Code's taxonomy holds up. "Feedback" sounds like a
one-off reaction, while these memories are standing guidance, and a directive covers confirmed approaches as well
as corrections.

### Overwrite in place, keep an audit log

A changed fact rewrites its file, and a wrong one is deleted. Every change goes to `changes.jsonl` with the text
before it, and `history` and `--restore` read that log. Old versions never enter context on their own. **Why:**
Byron chose Claude Code's model over versioned or "superseded" memories. Stale versions in context mislead agents,
but losing them outright makes mistakes unrecoverable, so the history is kept for people.

### Hooks, MCP and files only

Recall, extraction and session start run as hooks. The agent writes through MCP tools rather than file tools.
**Why:** both tools support the same hook JSON and MCP. Writing through MCP also works in Codex, whose sandbox may
not reach `~/.spindle`.

### Side models through the user's own CLIs

Model calls run as `claude -p` or `codex exec`, with tools, settings and saved sessions turned off. **Why:** no API
keys or billing of spindle's own, and it uses whatever models the user already has.

- **The selector uses the session's own CLI.** On Claude that is Sonnet, as in Claude Code. In a 20-message test
  Haiku tied with Sonnet on quality and was only about 0.4 s faster, so Sonnet stays until a larger test says
  otherwise.
- **Extraction and dreaming use Codex with Luna first, then fall back to Claude.** This was Byron's call. These
  passes run in the background, so latency doesn't matter there, and Claude is kept as a dependable fallback.
  Selection holds up the user's message, so it stays on the session's own CLI.

### One client, two interfaces

`memory.Client` does everything. The MCP tools and the CLI are thin wrappers that print the same text. **Why:** a
person should be able to do and see whatever an agent can, and the two interfaces can't drift apart if they share
one implementation.

### Projects by meaning, all visible everywhere

A project is a named store ("spindle", "eldspire"), not a repository. Every memory is named `<project>/<name>`,
and recall, search and extraction see every project. A folder only hints at the session's project. The hint picks
what the session starts with and where an unnamed write goes. **Why:** Byron wants work across many projects to
feel fluid, with agents aware of all the context. Keying memory to repositories split one body of work across
worktrees, and hid rules that apply everywhere.

- **Global** is a store like any other, loaded in every project, for who the user is and for rules like "never
  add Claude attribution". User-type memories go there by default.
- **Session start lists the other projects** but doesn't load their indexes. **Why:** context stays inside the
  10,000-character hook cap, while the agent still knows what exists. Recall and search reach the rest.
- **The selector gets session context**: the folder, the project, the projects already recalled from, and recent
  messages. **Why:** with many projects, the same words can mean different things. Context helps the selector
  rank without hiding anything.
- **A write never links a folder.** **Why:** a write can be about any project. In testing, auto-linking on the
  first write tied Byron's spindle folder to "eldspire". Links are made on purpose, with `memory_project` or
  `spin project --link`.
- **No stored "current project."** The hint is worked out again on every call. **Why:** there's nothing to switch
  and nothing to go stale. A link made mid-session takes effect at once.

### Dreams on a schedule

Neither tool has a scheduled hook, so a launchd agent runs `spin dream --due` hourly. The worker also checks after
each drain. **Why launchd over cron:**

- launchd catches up on runs missed while the Mac slept, and cron doesn't;
- it runs as the user without needing Full Disk Access;
- it's the system scheduler that Apple supports.

Each store's own gate (24 h and 5 sessions) decides whether anything runs, so frequent checks cost nothing.

### Late recall goes after a tool call

The prompt hook waits only 4 s. Picks that arrive later are attached after the next tool call, and dropped if the
turn ends first. **Why:** this is what Claude Code does. A slow selector never delays the user's message, and late
picks are rarely lost.

### Leave the built-in memories on

On Byron's machine, Claude Code's auto memory and Codex's memories stay on alongside spindle, by his choice.
`spin install claude` still prints `autoMemoryEnabled: false` as an option, so people can avoid two memories
writing about the same session.

### An activity log for people

`events.jsonl` records every start, pick, save, skip, extraction and dream, with timings and reasons. **Why:**
memory that works invisibly is hard to trust or debug, and `spin log -f` makes it watchable. It is never read back
into context.

## Not built yet

- **A continuation prompt at compaction.** Byron's principle is that compaction should produce a continuation
  prompt, with decisions as only one part of it. Spindle holds durable facts only for now.
- **Usage ranking.** Codex ranks memories that get cited higher. Here the selector simply sees the newest 200 files.
- **Scaling past 200 candidates.** With many projects, older files fall out of the selector's view; search still
  finds them.
- **Cloud sessions.** Claude.ai project threads and other cloud sessions never run local hooks, so spindle only
  sees local sessions. A remote store (for example R2 or Postgres) would fix that.
- **A first-look gate before extraction**, beyond the 20-character rule.
- **Quality evals for this version.** The unit tests cover mechanics. Recall and extraction quality have been
  checked by hand end to end in both tools, not measured.
