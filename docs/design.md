# spindle design

## Purpose

Agent sessions are disposable, but the work done in them isn't. spindle turns raw agent transcripts (Codex, Claude Code) into durable memory, so that you or an agent can answer questions about past work correctly, as of now, and honestly about who decided what.

## What v0 optimizes for

When someone asks about a past **decision** ("what did we decide on X?", "did I agree to that?", "why?", "what did I reject?"), the answer should be:

- **correct**: it matches what actually happened
- **current**: it reflects the latest decision, not a replaced one
- **attributed**: it says whether the user decided, endorsed, or never engaged with an agent's proposal
- **grounded**: it cites transcript items that support it

It should come from the simplest mechanism that beats `rg` over raw transcripts. Cost is tracked on every run.

## Principles

1. **Transcripts are the only ground truth.** Everything else is derived from them and can be rebuilt. Raw transcripts are kept.
2. **Sessions are sources, not keys.** One session touches many topics, and many sessions touch one topic.
3. **Start simple; add complexity only when an eval failure calls for it.** Each addition is its own experiment against the simpler version.
4. **Memory is plain text.** Structure is limited to what code has to act on: `id`, `time`, `project`, `evidence`.
5. **Don't store what's cheap to rediscover.** Code locations, the current text of a rule, and anything `git` can answer stay out. Point at entities, not lines.
6. **Moving on is not agreeing.** Agent proposals the user never engaged with are recorded as such.

## Verbs

- `sleep`: transcript chunks → memory notes. v0 only extracts decisions.
- `dream`: consolidation across notes. Not built yet.
- `wake`: recall. v0 uses `rg` over Markdown files; there is no tool yet.

## v0 memory: decision notes

One Markdown file per decision:

```
---
id: d-7f3a91c2
time: 2026-09-15T22:10:00Z
project: zaum
evidence: [codex:01a07de8#L315, codex:01a07de8#L321]
---
Eldspire result bands: 1–3 fail, 4–5 mixed, 6+ clean (same as Blades).
The agent had proposed "5-only mixed" to match the old probabilities; the user
never agreed, and restored the Blades bands. Replaces the 5-only rule.
```

- **A decision** is a choice about how something should be: a design, a rule, an approach, or a scope. A rejection is a decision the user turned down.
- **The body is prose:** the choice, who proposed it, the user's stance (stated, endorsed, adopted, never addressed, rejected, corrected), the rationale if one was stated, and what it replaces.
- **Evidence** is a list of transcript item IDs (see `corpus.md`).

Not in v0: other record types (attempts, constraints, open threads, sources), an index, a database, an MCP tool, or structured fields beyond the four above. Each of these waits for an eval failure that justifies it.

## Design ladder

| Arm | Memory available to the agent |
|---|---|
| D0 | Nothing |
| D1 | Raw normalized transcripts (before the cutoff) |
| D2 | D1 plus decision notes |
| D2n | Decision notes only |

Later arms (not built): a generated index, ranked search, a `wake` MCP tool, `dream` consolidation, and live `sleep`.
