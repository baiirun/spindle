# spindle

Memory for agent sessions. spindle turns raw agent transcripts (Codex, Claude Code) into durable, searchable memory.

- `sleep`: turns transcripts into durable episodes and records
- `dream`: consolidates across episodes into current knowledge
- `wake`: recall with citations back to transcript items

Status: pre-alpha. The first goal is one design that runs end to end and can be evaluated against real recall questions.
