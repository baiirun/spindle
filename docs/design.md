# spindle design

## Purpose

Spindle lets a fresh agent recover the context needed to continue long-running
work after the original session or agent is gone. Raw transcripts and artifacts
remain the evidence source; Spindle creates compact, rebuildable projections and
gives agents read-only ways to discover and expand them.

## Model

```text
raw session events and artifacts
  → durable, evidence-linked episodes
  → resume a known session, or wake/search/read during cold start
  → optional consolidation when episode navigation no longer stays bounded
```

Sessions are sources, not the identity of an effort. `prog` tasks are optional
links: useful coordination signals, but not the authority for context and not a
requirement for work to be recorded.

## Principles

1. **Raw sources are the ground truth.** Projections are derived and rebuildable.
2. **Agents do not write memory.** An external scheduler or harness calls
   `sleep`; agents only use read tools.
3. **Every durable claim cites source items.** A projection MUST NOT make an
   unsupported claim of agreement, completion, or currentness.
4. **Carry context by reference.** An episode MAY point to a prior episode,
   artifact, task, or source span, but MUST NOT silently restate carried context
   as a fresh observation.
5. **Start with navigation, not ontology.** Lexical search and explicit links
   come before embeddings, a graph database, or a global mutable state object.
6. **Optimize for actual continuation.** The measure of success is whether a
   fresh agent can find relevant context, preserve constraints, and take useful
   next action with less rediscovery than raw history alone.

## Verbs

- `ingest`: normalize durable transcript sources into Markdown chunks with
  stable item IDs.
- `sleep`: refresh one session's transcript in the corpus, then project it into bounded handoff episodes (`spin sleep <source>:<session>`). The
  external caller decides when a session is idle or complete.
- `resume`: warm-start a replacement agent from the latest usable episode of a
  known source session. It exposes trailing source-only ranges instead of
  presenting a failed compaction as a handoff; an unprojected corpus session
  falls back to its raw chunks.
- `wake`: return compact matching episodes and source passages for a scope and
  query when there is no known prior-session handle.
- `search`, `read`, `related`: let an agent expand beyond an episode without
  receiving a giant fixed briefing.
- `dream`: not built. It is justified only when episodes and link-following no
  longer produce a bounded continuation context.

The legacy decision-note experiment remains in `data/memory/decisions-v2` for
comparison. It is not the current product data model: its one-note-per-local
choice projection over-extracts routine work and unconfirmed suggestions.
