# spindle

Durable, searchable context for agent work. spindle projects raw agent
transcripts into evidence-linked episodes that a fresh agent can search and
expand when it needs to continue work.

- `sleep`: background projection of a completed or idle session
- `wake`, `search`, `read`, `related`: read-only continuity tools for agents
- `dream`: future consolidation across episodes, only when needed

Status: pre-alpha. The first goal is a real end-to-end continuation loop; see
[the episode contract](docs/episodes.md).
