# Episode projection contract

## Purpose

An episode is a durable, compact handoff projection of one bounded transcript
range. It lets a later agent orient itself and locate proof without treating a
lossy summary as the source of truth.

## Scope

`sleep` currently projects one normalized chunk at a time. A normal session
with one chunk produces one episode; a long session produces consecutive,
explicitly linked episodes. The external scheduler chooses when to call it.

This contract does not create a universal effort-state record, a task system,
or a live agent inbox.

## Behavior contract

- An episode MUST identify its source, session, bounded source range, scope,
  and source hash.
- An observation, output, open thread, or reference MUST cite one or more item
  IDs inside its source range. Unsupported generated entries are discarded.
- An episode MUST remain rebuildable from its source range and prompt version.
- Consecutive ranges from a source session MUST link to the preceding episode.
- An external caller MAY supply typed continuation links. They MUST be labeled
  as carried context, not as observations from the current source range.
- Agents MUST NOT write episodes. The read interface is `wake`, `search`,
  `read`, and `related`.
- `wake` SHOULD return episode matches before raw transcript matches, while
  leaving raw evidence available for verification.

## Persisted format

Episodes are plain Markdown under
`data/episodes/episodes-v2/<source>/<session>/<chunk>.md`. Frontmatter holds
the stable identity, source range, scope, source hash, projection hash, and continuation links.
The body holds purpose, observations, outputs, open threads, and references,
with inline transcript citations.

## Interfaces

```text
spindle sleep --source codex --session <session-id>
spindle wake --scope spindle --query "durable compaction"
spindle search --scope spindle --query "episode format"
spindle read episode:codex/<session-id>/0001
spindle related episode:codex/<session-id>/0002
```

`sleep --continue REF=WHY` lets the external scheduler carry a known episode,
artifact, task, or source reference into the first episode of a new session.

## Implementation model

The episode module owns source-range validation, persistence, and link format.
The wake module owns lexical discovery and safe reference expansion. The CLI is
a thin adapter over both modules, so an eventual read-only MCP adapter can use
the same interface without duplicating retrieval behavior.

## Verification

Run `go test ./...` and `go vet ./...`. The episode tests prove source-bound
citations are retained while invented IDs are discarded, and that continuation
links are persisted. The wake tests prove episode-first lexical ranking, safe
reference reads, and explicit-link traversal.

For product validation, run `sleep` after a real session, start a fresh agent
with only the read tools, and inspect whether it follows episodes and evidence
to continue useful work. The harness, not the agent, records that trace.
