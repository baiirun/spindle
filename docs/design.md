# spindle design

## Purpose

Spindle lets a fresh agent pick up long-running work (efforts that run for weeks, in
concrete or exploratory form) after the original session or agent is gone. It must
answer, for a project: what has been done, what hasn't, what was tried, what was
decided, what is open, and where to continue, and let the agent verify each answer
against what actually happened.

## Scope

Spindle covers recording agent sessions, projecting them into durable shapes, and
read-only retrieval for agents. Non-goals:

- **Task management.** `prog` owns the DAG of tracked work. Spindle links to tickets
  when they exist, never requires them, and never writes them.
- **Resuming a live thread.** When the original thread is available, reopening it is
  cheaper than any projection (see `results.md`, native arm). Spindle is for starting a
  new session on existing work.
- **Agent-written memory.** Agents read; an external harness or scheduler writes.

## Model

Spindle is event-sourced. Sessions are the event log; everything else is a projection
that can be rebuilt from it.

```text
project (given by the harness)
  sessions: one long-lived coordinator session + worker sessions   ← event log, source of truth
     │ sleep
     ▼
  episodes: bounded, cited summaries of stretches of a session     ← index + compact recall
     │ dream (fold, in event order)
     ▼
  project memory                                                   ← current working state
    - working state: active threads, each with latest state, decisions,
      what was tried, and the next step
    - history index: one line per past stretch of work, with a pointer
    - superseded log: what changed and why, with pointers
```

- **Project** is the bucket. The harness decides it: a session is started from a
  project, as in Claude projects, so spindle never infers membership. A project has a
  long-lived **coordinator session** (like the Eldspire TTRPG log or a project thread)
  and **worker sessions**.
- **Episodes** say what happened during a stretch of one session. They are the evidence
  index, not the current state.
- **Project memory** says where the project stands now. It is a fold over the project's
  episodes in event order, so its state as of any time T is well defined and can be
  rebuilt from episodes before T.
- **Efforts**: a thread inside the project that grows too big for the working state MAY
  be split into its own effort record with the same shape. Splitting is a measured
  outcome, not an up-front rule.
- **Cross-project memory** is limited to pointers: which other projects hold related
  work, and why. There is no merged global summary.

## Behavior contract

1. Raw sessions MUST remain the source of truth. Every projection MUST be rebuildable
   from sessions alone.
2. Every durable claim in an episode or project memory MUST cite source items. Claims
   of agreement, completion, or currentness MUST NOT go beyond that evidence.
3. Project memory MUST keep history discoverable: past work that leaves the working
   state MUST remain reachable through the history index, as a brief description plus
   a pointer.
4. Project memory MUST NOT silently rewrite a belief. A changed decision or status MUST
   be recorded in the superseded log with a pointer to the evidence for the change.
5. A projection as of time T MUST use only events before T. This keeps historical
   evaluation leak-free.
6. Agents MUST NOT write memory. `sleep` and `dream` run from a harness or scheduler.
7. Retrieval SHOULD default to the session's project: `wake` and `resume` scope to it,
   and cross-project lookup is explicit.
8. A new session SHOULD start from project memory, then drill into episodes and raw
   transcripts through pointers before stating anything it will act on.
9. Spindle MUST NOT create or modify `prog` tickets. It MAY read them as signals and
   link to them.

## Interfaces

CLI `spin`; data under `$SPINDLE_HOME` (default `~/.spindle`).

| Verb | Status | Contract |
|---|---|---|
| `ingest` | built | Normalize Codex and Claude transcripts into Markdown chunks with stable item IDs (`codex:<id8>#L<line>`). |
| `sleep <source>:<session>` | built | Refresh one session's transcript, then project new or changed chunks into episodes. `--before T` projects only chunks that ended before T. |
| `resume --source S --session ID` | built | Return the latest usable episode of a known session plus the raw tail after it. |
| `wake`, `search`, `read`, `related` | built | Discover and expand episodes and transcript passages. Ranking is lexical, over whole chunks. |
| `dream` | proposed | Fold new episodes into project memory, with the contract above. |
| project catalog | proposed | List the harness's projects, each with its memory and recent sessions (one-line card each). |

Stored entities: `corpus/<source>/<session>/NNNN.md` (chunks, derived from raw
transcripts), `episodes/<version>/<source>/<session>/NNNN.md` (episodes). Project
memory and the catalog are proposed; their on-disk shape is not fixed yet.

## Implementation model

- **Episodes stay slice-sized** (~10k-token chunks, projected in parallel) because they
  are the evidence index. The eval showed they drop about half of the facts a handoff
  needs: a slice never sees the whole arc. So current state belongs in project memory,
  not in the latest episode.
- **Project memory is a fold, not a rewrite.** `dream` reads the current memory plus
  the episodes since its last watermark, and emits the next memory. This mirrors Codex
  v2's whole-session "final state" summary and Mastra's observation log, but at project
  level, with citations kept and supersession recorded instead of overwritten.
- **Discovery is catalog-first.** Within a project, the project memory and a list of
  recent sessions come before keyword search. On our trials, recency alone put the right
  session in the top 3 in 61/61 cases, while chunk search found it about 60% of the time.
- **Why not one file per effort from the start:** agent-maintained effort files risk
  wrong merges, dropped failed attempts, and stale text that looks authoritative. One
  project memory with active threads, split only when a thread outgrows it, keeps
  identity decisions few and reviewable.

## Verification

- **Continuation trials** (`docs/eval.md`, `spin eval trials`): a fresh agent at a real
  pickup moment writes a handoff brief, which is graded against verified pre-cutoff
  claims. Arms: cold floor, known handle, discovery from a vague opener, and native
  thread resumption as a reference.
- **Project memory as of T**: fold each project's episodes up to every trial cutoff.
  Check (a) whether the memory alone contains the must-know claims (one judge call per
  trial), and (b) whether an agent starting from the project catalog, then the memory,
  then `resume` beats the current discovery score (0.47 coverage, 61% sessions found).
- **Long-gap slice**: pickups after days or weeks with other sessions in between, e.g.
  the Eldspire coordinator session, 9 trials from 07-07 to 08-06. Most current trials
  are short-gap, which flatters recency and raw search.
- **Leak check**: every claim a projection cites MUST resolve to an item before its as-of
  time (`trials-verify` enforces this for answer keys; the same check applies to memory).

## Notes

- Evidence so far (`docs/results.md`): with a known handle, raw, episodes and native all
  recover about 80% of verified claims, and the cold floor is 0.05 once the request text
  doesn't leak. With a vague opener, finding the session is the bottleneck. Telling the
  agent to `resume` the session it finds lifted episodes from 0.36 to 0.47. In the
  month-long Eldspire coordinator session, episodes beat raw (0.78 vs 0.67), matching
  native (0.75).
- Open questions: the on-disk shape and size limit of project memory; when a thread
  becomes its own effort; how `dream` is scheduled (per session end, or per N new
  episodes); how the coordinator session's own transcript is treated relative to worker
  sessions.
- The legacy decision-note experiment (`data/memory/decisions-v2`) is not the product
  model: one note per local choice over-extracted routine work and unconfirmed
  suggestions.
