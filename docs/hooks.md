# Live project memory for Claude Code and Codex

spindle can run as the memory of a long-running project coordinator in either tool. It uses only seams both tools
share: three hooks, an MCP server and files under `~/.spindle`. The memory is shaped like the two-half design in
`variants.md`: a **Now** summary rewritten fresh each time, a **decision record** carried forward by add / replace /
drop edits and searched rather than shown, the user's messages verbatim, and pointers back to the full transcripts.

Status: built and unit-tested on fake transcripts and a fake model. Not yet run against a real session or scored.

## What runs when

| Seam | Hook | What spindle does | Cost to the session |
|---|---|---|---|
| Session starts, resumes, clears, or has just compacted | `SessionStart` | Injects the Now summary, the record's index and, on a fresh start, the user's latest messages from earlier sessions. Queues a fold of any earlier session with unfolded turns. | Reads files; milliseconds |
| User sends a message | `UserPromptSubmit` | Searches the record and past user messages for the prompt's words and injects the strong matches, each once per session. Bare openers ("go", "keep going") match nothing. | Local search, capped at 1.5 s; past the deadline it reuses the previous turn's block |
| Turn ends | `Stop` (async) | Writes a job file and starts the worker if none is running. | None: runs in the background |
| Before compaction | `PreCompact` | Queues a forced fold, so the Now summary catches up. | Writes a job file |
| Session ends | `SessionEnd` | Same as PreCompact. Codex allows at most 3 s here, so it only queues. | Writes a job file |
| Any time | MCP tools | `memory_search`, `memory_now`, `memory_write`, `memory_drop`, `memory_history`, `transcript_search`, `transcript_read` | On demand |

Neither tool can inject context at compaction itself, so context goes back in through `SessionStart` with source
`compact`. After a compaction the tool's own summary already holds that session's messages, so spindle adds only
the Now summary and the index.

## The worker

`spin memory worker` drains the queue and exits; hooks start it detached. One runs at a time (an flock on
`worker.lock`). For each transcript it:

1. reads the new turns past its cursor, with spindle's existing Claude Code or Codex parser (told apart by content);
2. skips them if they are under 12,000 characters and nothing forced the fold, so short turns don't cost a model call;
3. asks the model for record edits (the searchable-record prompt, the best on Eldspire recall), with cites checked
   against the transcript's item IDs;
4. rewrites the Now summary from the session's last 150,000 characters, with the latest ask and reply copied verbatim;
5. appends the new user messages to `users.jsonl` and refreshes the transcript's corpus chunks for `transcript_search`.

Model calls run without the project lock; edits are applied under it, so an MCP `memory_write` never waits minutes.
The model is the pinned eval model through Codex CLI (`internal/llm`); `SPINDLE_MODEL` overrides it. Every process
the worker starts carries `SPINDLE_INNER=1`, and hooks that see it do nothing, so a Codex model call can't fire
spindle's hooks again.

## Dreaming

Neither tool has a scheduled hook, so the worker checks a gate after each drain: at least 24 hours and at least 5
sessions since the last pass (the same gate as Claude Code's auto_dream). The pass reads the whole record and marks
entries that newer ones overtook: it narrows an entry that still partly holds and drops one that was superseded,
naming the newer entry. It never adds entries and never drops one for age or size, which is what cost the
consolidating ledger its recall. `spin memory dream --force` runs it by hand.

## Files

Each project (the git top level of the session's `cwd`, or `SPINDLE_PROJECT`) gets `~/.spindle/projects/<name>-<hash>/`:

| File | What |
|---|---|
| `now.md` | The Now summary, with the time, session and transcript path it was written from |
| `record.json` | Record entries and a log of every replace and drop |
| `users.jsonl` | Every user message, verbatim |
| `state.json` | Per-transcript cursors and the dream gate |
| `recall/<session>.json` | What was already injected in that session |

Hook errors go to `~/.spindle/hooks.log`, worker output to `~/.spindle/worker.log`. A hook always exits 0, so a
memory failure never blocks a session.

## Install

`spin memory install claude` and `spin memory install codex` print the config to merge; nothing is written.

- **Claude Code:** the hooks go in `~/.claude/settings.json` with `autoMemoryEnabled: false`, so the built-in memory
  doesn't compete. The MCP server is added with `claude mcp add --scope user spindle -- spin mcp`.
- **Codex:** the same hooks go in `~/.codex/hooks.json`, and they need trusting with `/hooks`. The MCP server goes in
  `config.toml` under `[mcp_servers.spindle]`. Codex's own memories are off by default.

Injected context stays under 9,000 characters at session start and 3,000 per prompt, below Claude Code's
10,000-character cap and Codex's default of about 2,500 tokens.

`spin memory status` shows a project's state; `spin memory fold <transcript>` folds one transcript by hand.

## Known gaps

- Folding lags the conversation: a SessionStart right after a PreCompact shows the Now summary from before the
  compaction if the worker hasn't finished.
- One Now summary per project. With two sessions open on the same project, the last fold wins.
- Prompt-time recall is lexical. It misses paraphrases; the model can still call `memory_search`.
- The record prompt is the eval's searchable record (v1), not record v2: v2's strict approval gate lost recall.
