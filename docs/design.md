# spindle: project memory for Claude Code and Codex

A prototype built from first principles on what Claude Code's and Codex's own memory systems do. It ignores the
earlier spindle code and evals. It runs entirely through seams both tools share: hooks, an MCP server and files.

## Shape

It follows Claude Code's auto memory, with one change of name.

- **One folder per project** (the git top level of the session's directory): `~/.spindle/projects/<name>-<hash>/memory/`.
- **One Markdown file per subject**, in Claude Code's format: frontmatter `name`, `description` (one line, used to
  decide relevance) and `metadata.type`, then the body. For directive and project memories the body leads with the
  rule or fact, then a **Why:** line and a **How to apply:** line.
- **Four types:** `user` (who the user is), `directive` (Claude Code's "feedback": corrections and confirmed
  approaches), `project` (goals, decisions, constraints the code doesn't show) and `reference` (where things live).
- **MEMORY.md** is the index, one line per file (`- [name](file.md) — description`), grouped by type and capped at
  Claude Code's 200 lines and 25 KB. Code rebuilds it after every change, so it can't drift from the files.
- **Changes overwrite in place**, and wrong memories are deleted. Every write and delete also goes to
  `changes.jsonl` with the old text, for audit only. Nothing reads that log back into context.

## When things run

| When | Hook | What happens |
|---|---|---|
| Session start, resume, clear, after compaction | `SessionStart` | Adds the memory instructions (Claude Code's memory section, cut to fit) and MEMORY.md. After a compaction or clear, files shown earlier may be shown again. |
| Each user message | `UserPromptSubmit` | Starts the selector, which is Claude Code's recall prompt, word for word, over the 200 newest files as `[type] file (mtime): description`, picking at most 5. The hook waits up to 4 s (`SPINDLE_RECALL_WAIT`). If the picks are ready in time, they go in with the prompt. |
| After a tool call | `PostToolUse` | Attaches picks that arrived after the prompt went in. This is how Claude Code delivers them. |
| Turn ends | `Stop`, async | Drops picks that never arrived, queues extraction and starts the worker. |
| Before compaction, session end | `PreCompact`, `SessionEnd` | Queue extraction. |
| Any time | MCP | `memory_read`, `memory_write`, `memory_delete`, `memory_search`, `memory_index`, `memory_status` |

Recall keeps Claude Code's rules. One-word prompts are skipped. A file is shown at most once per session. Recall
stops after 61,440 bytes in a session. Each file is cut at 4 KB. Files older than a day carry the "This memory is N
days old" warning, and the first file carries "Retrieved for possible relevance". `SPINDLE_RECALL=local` swaps the
model for word matching in the hook itself.

The main agent writes memory through the MCP tools, not file tools, so the same instructions work in Codex, whose
sandbox may not reach `~/.spindle`.

## One client, two interfaces

The memory tools and the CLI are thin wrappers over one client (`memory.Client`): read, write, delete, search,
index and status, with the same output text. So `spin show`, `spin write`, `spin rm`, `spin search`, `spin index`
and `spin status` do what `memory_read`, `memory_write`, `memory_delete`, `memory_search`, `memory_index` and
`memory_status` do, for people instead of agents. Writes from the CLI are logged as `cli`, and unlike the agent's,
they don't make extraction skip a session.

## Activity log

Each project keeps `events.jsonl` next to its memory folder, for people reading along. Nothing reads it back into
context. It gets one line per session start, recall pick (the prompt, what was picked, how long it took), recall
delivery (with the prompt or after a tool call), skipped recall or extraction and why, memory call (read,
write, delete, search, index, with the caller: claude, codex or cli), extraction pass and dream. `spin log` prints it as
readable lines; `-f` follows it, `--session` and `--kind` filter it and `--json` prints the raw lines. Saves and
deletes also stay in `changes.jsonl` with the old and new text.

## Extraction

The worker drains the queue (one worker at a time, under an flock) and runs one pass per transcript over the
messages after its cursor, as Claude Code does after each turn. It skips the pass when the user wrote under 20
characters, or when the main agent already wrote memory during those messages. Claude Code's extractor plans "read
everything first, then write", so here that's two model calls:

1. The model sees the file list and the new messages. It answers with `nothing_to_save`, or with the files to read.
2. It gets those files and returns `write` or `delete` operations, which code applies.

## Dreaming

Dreaming is Claude Code's auto_dream gate and phases (orient, gather, consolidate, prune, reconcile against
CLAUDE.md). It runs at most once per 24 hours, after 5 sessions have touched the project. The worker checks the gate
after each drain, since neither tool has a scheduled hook. Without tools, the model is given every memory file, the
project's CLAUDE.md and AGENTS.md, and the messages from the sessions since the last dream. It returns writes,
deletes and a summary, and the summary goes to `dreams.jsonl`. `spin dream --force` runs it by hand.

## Models

Side calls go through the CLI the user already has signed in: `claude -p` (no tools, no settings, no saved session)
or `codex exec` (read-only, ephemeral). The CLI is chosen by the agent whose hook started the work, or by
`SPINDLE_BACKEND`.

| Role | Claude default | Codex default | Override |
|---|---|---|---|
| select | sonnet | gpt-6-luna, low effort | `SPINDLE_MODEL_SELECT` |
| extract | sonnet | gpt-6-luna, low effort | `SPINDLE_MODEL_EXTRACT` |
| dream | opus | gpt-6-luna, medium effort | `SPINDLE_MODEL_DREAM` |

As in Claude Code, Sonnet selects. Through the CLI, Sonnet 5.5 and Haiku 5.5 take about the same time to start
(3.0 s and 2.6 s for a one-word reply), so Haiku buys little here. The selector's full run, with the file list, took
about 6 s in one test, which is past the 4 s wait, so those picks arrive after the next tool call.

Every process spindle starts carries `SPINDLE_INNER=1`. Hooks that see it do nothing, so a side call can't fire
spindle again.

## Install

`spin install claude` and `spin install codex` print the config to merge; nothing is written for you. Claude Code
gets `autoMemoryEnabled: false`, so its own memory doesn't also run. Codex's own memories are off by default.

## Not in this prototype

- A continuation prompt at compaction. Memory here holds durable facts only, as in Claude Code.
- Codex's usage ranking, where cited memories rank higher in consolidation. The selector sees the newest 200 files.
- A first-look gate before extraction, beyond the 20-character rule.
