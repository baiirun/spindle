# Evaluation (legacy decision-recall experiment)

This document describes the earlier decision-note experiment. It is retained as
evidence and a source of historical recall candidates, but is not the target
evaluation for the episode-continuity product. The next evaluator should run
fresh agents at historical session boundaries, give them only `wake/search/read`,
and score their retrieval trace and ability to continue useful work.

## Continuation trial

`spin eval continue --source <source> --session <id> --task "..."` runs one
fresh Luna agent from the Spindle checkout. Its startup instruction contains the
known prior-session handle and requires `spin resume` before it works. The
run records the answer and tool trace in `runs/.../continuation.json` and fails
if the agent does not successfully invoke `resume`.

This is an end-to-end smoke trial, not an isolated benchmark: the current
Codex CLI sandbox can read the checkout, and there is no MCP adapter or
automated scheduler handoff yet.

## What we measure

Two layers:

1. **Component:** given its memory, can an agent answer a decision question correctly?
2. **Behavioral:** does the agent reach for memory, search well, and use what it finds? v0 measures this from the traces of the same runs.

## Eval items

Items are real recall questions the user asked Codex. They're mined from transcripts, and their gold answers are drafted from what happened next in the session (the answer, plus any follow-up correction from the user).

```json
{
  "id": "q-0012",
  "question": "what did we decide on for variant successes?",
  "asked_at": "2026-07-21T18:03:00Z",
  "session": "codex:019fe1fc",
  "project": "zaum",
  "kind": "current|who|why|changed|rejected",
  "gold": ["Doubles on a success are exceptional.", "The user never agreed to X; the agent proposed it."],
  "same_session": true,
  "confidence": "high"
}
```

- **Cutoff:** the arm only sees chunks whose `end` is at or before `asked_at`. The session the question was asked in is truncated to the items before the question. This enforces no leakage.
- **Fresh agent:** the reader gets the question with no session context, only memory. Recalling something from the same session is still valid, because it's exactly what context compaction loses.
- **Labels:** v0 labels are drafted by a model and reviewed by the builder. The user spot-checks them later. Only `confidence: high` items are scored.

## Runs

For each item and arm, spindle builds a directory of the memory available at the cutoff (hard links, plus a truncated copy of the current chunk). It then runs a headless agent in that directory with read-only file tools (`Read`, `Grep`, `Glob`) and a fixed prompt.

| Role | Model | Notes |
|---|---|---|
| Extractor (`sleep`) | gpt-6-luna, low reasoning | No tools; outputs JSON |
| Reader (agent) | gpt-6-luna, low reasoning | Current directory is materialized memory; host-read isolation and a turn cap are deferred in this Codex CLI experiment |
| Judge | gpt-6-luna, low reasoning | No tools; outputs JSON; frozen prompt |
| Labeler | gpt-6-luna, low reasoning | Drafts gold answers |

## Scores

Each is reported overall and sliced by `kind`, by `same_session`, and by arm.

| Score | Definition |
|---|---|
| **correct** | Share of gold statements the answer supports (judge) |
| **contradiction** | The answer states something that conflicts with the gold (judge) |
| **attribution** | For `who` items, whether the answer states who decided it correctly (judge) |
| **abstained** | The answer says it doesn't know |
| **citations valid** | Share of cited item IDs that exist in the memory available to that run (deterministic) |
| **reached** | The agent searched memory at all (from the trace) |
| **usage** | Codex input, output, and reasoning tokens, plus turns and duration per item; local runs do not estimate USD cost |

A change is kept when it improves `correct` without making `contradiction` worse, and the improvement is larger than the run-to-run noise measured by repeating an arm.

## Run log

Each run writes `runs/<timestamp>-<arm>/`: per-item traces, judgments, and `summary.json`. `runs/` is gitignored. Aggregate results, with no transcript text, are recorded in `docs/results.md`.
