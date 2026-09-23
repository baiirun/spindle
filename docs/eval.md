# Evaluation

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
| Extractor (`sleep`) | claude-sonnet-5 | No tools; outputs JSON |
| Reader (agent) | claude-sonnet-5 | Read-only tools, maximum 12 turns |
| Judge | claude-opus-5-5 | No tools; outputs JSON; frozen prompt |
| Labeler | claude-opus-5-5 | Drafts gold answers |

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
| **cost** | USD, turns, and duration per item |

A change is kept when it improves `correct` without making `contradiction` worse, and the improvement is larger than the run-to-run noise measured by repeating an arm.

## Run log

Each run writes `runs/<timestamp>-<arm>/`: per-item traces, judgments, and `summary.json`. `runs/` is gitignored. Aggregate results, with no transcript text, are recorded in `docs/results.md`.
