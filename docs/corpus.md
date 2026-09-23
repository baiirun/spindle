# Corpus

## Sources

- **Codex:** `~/.codex/sessions/**/rollout-*.jsonl` and `~/.codex/archived_sessions/rollout-*.jsonl`. These are flat, append-only JSONL files.
- **Claude Code:** `~/.claude/projects/<cwd-slug>/<session>.jsonl`. These are message trees, and Claude deletes them after about 30 days.

Only root sessions are ingested. Subagent threads are skipped.

## Normalized form

`spindle ingest` writes plain Markdown chunks to `data/corpus/<source>/<session>/<NNNN>.md`. The `data/` directory is gitignored, because transcripts are private.

```
---
source: codex
session: 01a07de8-ae9d-7291-a230-df7f5da2d1cd
cwd: /Users/.../Zaum
chunk: 3
start: 2026-09-15T21:02:11Z
end: 2026-09-15T22:40:05Z
---
[codex:01a07de8#L315 2026-09-15T22:10Z user] why is 5 mixed? i dont remember deciding on that
[codex:01a07de8#L317 2026-09-15T22:10Z assistant] You didn't decide that. ...
[codex:01a07de8#L318 2026-09-15T22:11Z tool] $ rg -n "mixed" ... → (truncated)
```

- **Item ID:** `<source>:<session-id prefix>#L<line>`, where `line` is the 1-based line number in the source JSONL. Source files are append-only, so IDs stay stable when the file is re-ingested.
- **Kept:** human messages; assistant messages; tool calls, cut to one line; tool outputs, cut to 300 characters.
- **Dropped:** injected harness context (AGENTS.md, environment, skills, permissions, subagent notifications), reasoning, and token or status events.
- **Chunks** hold about 40k characters of consecutive items. A chunk's `end` time decides when its contents become available to the eval, so cutoff filtering happens per chunk.

## Snapshot

The corpus is a snapshot. Every eval run records the ingest configuration it used. Re-ingesting only changes chunks whose source changed.
