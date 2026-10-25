package memory

// StartBudget keeps session-start context under Claude Code's 10,000
// character cap on hook output and about Codex's default 2,500 tokens.
const StartBudget = 9_500

// StartContext is what a session gets at start, resume, clear and after
// compaction: the memory instructions and the index, the counterpart of
// Claude Code loading its memory section and MEMORY.md. p is the session's
// context; the indexes cover its project, global, and a line per other project.
func (p Project) StartContext() string {
	text := Instructions + "\n\n" + p.indexText()
	if len(text) > StartBudget {
		text = clipRunes(text, StartBudget-80) + "\n(index cut here; memory_search and memory_projects find the rest)\n"
	}
	return text
}
