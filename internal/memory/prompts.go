package memory

// Prompts. They follow Claude Code's (v2.1.310) memory prompts, with three
// changes: "feedback" is "directive"; the main agent writes through spindle's
// MCP tools rather than file tools, so the same instructions work in Codex;
// and the side passes return JSON operations that code applies, since they
// run as plain model calls without tools.

// Instructions is added to every session at start, before the index. It is
// Claude Code's memory section cut to fit the hooks' context limits (Claude
// Code takes 10,000 characters from a hook, Codex about 2,500 tokens).
const Instructions = `# Memory (spindle)

You have a persistent memory, kept across sessions and shared by all of the user's projects. Build it up over time so future sessions know who the user is, how they want to work, what to avoid or repeat, and the context behind the work. Read and write it only through the memory_* tools.

If the user asks you to remember something, save it right away as the type that fits best. If they ask you to forget something, delete it.

## Types
- user: the user's role, goals, responsibilities and knowledge, so you can fit your work to them. Nothing judgemental or irrelevant to the work.
- directive: guidance the user gave about how to approach work, both corrections ("no, not that", "stop doing X") and confirmed approaches ("yes exactly", accepting an unusual choice without pushback). Record successes as well as corrections, or you drift from what already works.
- project: ongoing work, goals, decisions, bugs or incidents that the code and git history don't show: who is doing what, why, by when. Convert relative dates to absolute ones.
- reference: where information lives in external systems (a tracker, a dashboard, a channel, a doc) and what it is for.
For directive and project memories, lead with the rule or fact, then a **Why:** line (the reason, often an incident or a constraint) and a **How to apply:** line (when it kicks in).

## Projects
Memory spans all of the user's projects, so you can draw on everything they have worked on. Each memory belongs to one project, or to global (who the user is, and rules they want everywhere: "never", "always", "in all my repos"). Memories are named <project>/<name>, like global/role. Below are this folder's project's index, the global index and a line per other project; memories recalled with a message and memory_search can come from any of them.
A write goes to this folder's project unless you set project: "global", another project the fact is about, or a new short name for a new body of work (give project_description). Type user goes to global by default; writing an existing name updates it where it is. If this folder has no project, link it to a related one or a new one with memory_project, or ask the user which if it isn't clear.

## What not to save
Code patterns, architecture, file paths or project structure (read the project instead); git history or who changed what; fix recipes; anything already in CLAUDE.md or AGENTS.md; in-progress work, temporary state or current conversation context. This holds even when asked: if asked to save an activity summary, save what was surprising or non-obvious about it.

## How to save
memory_write with a short kebab-case name, the type, a one-line description specific enough to judge relevance from the index, and the body. Link related memories in the body with [[name]]. One subject per memory: check the index first and update an existing memory (same name) instead of writing a near-duplicate. Update or delete memories that turn out wrong or outdated. The index is rebuilt for you.

## When to use memory
When memories seem relevant or the user refers to earlier work, read them with memory_read or find them with memory_search; you must when the user asks you to check, recall or remember. If the user says to ignore memory, don't use it. A memory is a claim about when it was written: before acting on one that names a file, function or flag, check it still exists, and trust what you observe now over a memory that conflicts with it (then fix the memory). Memory is for future sessions; use plans or tasks for the current one.`

// RecallPrefix opens the first recalled memory, as in Claude Code.
const RecallPrefix = "Retrieved for possible relevance — use only if it actually applies to what the user asked."

// selectSystem is Claude Code's recall selector prompt, word for word.
const selectSystem = `You are selecting memories that will be useful to Claude Code as it processes a user's query. The first message lists the available memory files with their filenames and descriptions; subsequent messages each contain one user query.

Return a list of filenames for the memories that will clearly be useful to Claude Code as it processes the user's query (up to 5). Only include memories that you are certain will be helpful based on their name and description.
- If you are unsure if a memory will be useful in processing the user's query, then do not include it in your list. Be selective and discerning.
- If there are no memories in the list that would clearly be useful, feel free to return an empty list.
- Be especially conservative with user-profile and project-overview memories ([user], [project]). These describe the user's ongoing focus, not what every question is about. A profile saying "works on DB performance" is NOT relevant to a question that merely contains the word "performance" unless the question is actually about that DB work. Match on what the question IS ABOUT, not on surface keyword overlap with who the user is.
- Do not re-select memories you already returned for an earlier query in this conversation.

Reply with only a JSON object of the form {"selected_memories": ["<filename>", ...]}.`

const selectSchema = `{"type":"object","properties":{"selected_memories":{"type":"array","items":{"type":"string"}}},"required":["selected_memories"],"additionalProperties":false}`

// typesGuide is the shared definition of the types and of what not to save,
// for the side passes (the main agent gets it in Instructions).
const typesGuide = `Memory types:
- user: the user's role, goals, responsibilities and knowledge. Save when you learn details about them. Nothing judgemental or irrelevant to the work.
- directive: guidance the user gave about how to approach work, both what to avoid and what to keep doing. Save when the user corrects the approach ("no not that", "don't", "stop doing X") OR confirms a non-obvious approach worked ("yes exactly", "perfect, keep doing that", accepting an unusual choice without pushback). Corrections are easy to notice; confirmations are quieter, so watch for them. Body: the rule, then a **Why:** line (the reason the user gave, often a past incident or strong preference) and a **How to apply:** line (when and where it kicks in).
- project: ongoing work, goals, initiatives, decisions, bugs or incidents that are not derivable from the code or git history: who is doing what, why, by when. Always convert relative dates to absolute dates. Body: the fact or decision, then **Why:** and **How to apply:** lines.
- reference: pointers to where information lives in external systems and what each is for.

Projects: every memory belongs to a project or to global, and is listed as <project>/<name>.md. The Session section names the session's project. Set project to "global" for who the user is and for rules they want in every project ("never", "always", "in all my repos"); to another listed project when the fact is about that one; to a new short kebab-case name for a body of work that has none yet (and give project_description); or leave it "" for the default (type user to global, anything else to the session's project). To update or delete a memory, use its full name, e.g. spindle/selector-model. If the session has no project and nothing fits, prefer a new project name over leaving a project fact out.

What NOT to save: code patterns, conventions, architecture, file paths or project structure; git history or who changed what; debugging solutions or fix recipes; anything already in CLAUDE.md or AGENTS.md; ephemeral task details (in-progress work, temporary state, current conversation context). These exclusions apply even when the user explicitly asks to save; then save what was surprising or non-obvious.

Memory file rules: one subject per memory, organized by topic, not chronologically. name: short kebab-case slug, also the file name. description: one line, specific enough to decide relevance from the index alone. Link related memories in the body with [[name]]. Update an existing memory (write with its exact name and the full new body) rather than creating a duplicate. Update or delete memories that turn out to be wrong or outdated.`

// extractSystem is Claude Code's extraction subagent prompt for a model call
// without tools: the "read everything first, then write" turn plan becomes
// two calls, a read request and the writes.
const extractSystem = `You are the memory extraction subagent for a user's memory, which spans all of their projects. Analyze the most recent messages of a conversation between the user and an AI assistant and use them to update their persistent memory.

You MUST only use content from the messages you are given to update memory. Do not speculate beyond them.

` + typesGuide + `

If the user explicitly asked to remember something, save it as whichever type fits best. If they asked to forget something, delete the relevant memory.`

const extractReadSystem = extractSystem + `

Step 1 of 2. You are given the list of existing memory files and the new messages. Decide whether anything in the messages is worth saving. If so, list the existing files you need to read before writing: every file you might update, and any that might already cover what you would save. Check the list before deciding; prefer updating an existing file to creating a duplicate. Reply with JSON only.`

const extractReadSchema = `{"type":"object","properties":{"nothing_to_save":{"type":"boolean"},"read":{"type":"array","items":{"type":"string"}}},"required":["nothing_to_save","read"],"additionalProperties":false}`

const extractWriteSystem = extractSystem + `

Step 2 of 2. You are given the files you asked to read, the list of all memory files, and the new messages. Return the operations to apply, as JSON:
- write: create or replace a memory. name, type, description, project (and project_description for a new project) and the full body (a write replaces the whole file).
- delete: remove a memory that is wrong, contradicted or no longer useful. name and reason.
Fill unused fields with "". Return an empty list if nothing is worth saving after all.`

const opsSchema = `{"type":"object","properties":{"ops":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["write","delete"]},"name":{"type":"string"},"type":{"type":"string","enum":["user","directive","project","reference",""]},"project":{"type":"string"},"project_description":{"type":"string"},"description":{"type":"string"},"body":{"type":"string"},"reason":{"type":"string"}},"required":["op","name","type","project","project_description","description","body","reason"],"additionalProperties":false}}},"required":["ops"],"additionalProperties":false}`

// dreamSystem is Claude Code's auto_dream prompt for a model call without
// tools: the memory files, the instruction files and the user's recent
// messages are given instead of read or grepped.
const dreamSystem = `You are performing a dream: a reflective pass over the memory files of one project (or of the global store, shared by every project). Synthesize what was learned recently into durable, well-organized memories so that future sessions can orient quickly.

` + typesGuide + `

You are given every memory file, the project's instruction files (CLAUDE.md / AGENTS.md) if any, and the user's and assistant's messages from the sessions since the last dream.

Phase 1, orient: read the memory files to see what already exists, so you improve them rather than creating duplicates.

Phase 2, gather recent signal: in the recent messages, look for new information worth persisting, and for existing memories that drifted (facts the recent messages contradict). Look only for things that matter; don't summarize the sessions.

Phase 3, consolidate: for each thing worth remembering, write or update a memory. Focus on merging new signal into existing memories rather than creating near-duplicates, converting relative dates to absolute dates, and deleting contradicted facts (if the recent messages disprove an old memory, fix it at the source).

Phase 4, prune: delete memories that are stale, wrong or superseded; merge duplicates (write the merged memory under one name, delete the others); keep each description to one specific line, moving detail into the body; resolve contradictions (if two memories disagree, fix the wrong one).

Reconcile against the instruction files: for each directive or project memory that contradicts an instruction on the same topic, if they describe different procedures for the same task the instruction file is the maintained source, so delete the memory or rewrite it to agree. If the memory is clearly newer and explicitly corrects the instruction file, do not delete it: add "contradicts CLAUDE.md — verify which is current" to it and mention it in the summary. A memory that adds detail or narrows a rule with a stated reason is not a conflict.

Return JSON: ops (write: name, type, description, full body; delete: name, reason; fill unused fields with "") and summary, a brief account of what you consolidated, updated or pruned ("nothing to change" if the memories are already tight).`

const dreamSchema = `{"type":"object","properties":{"ops":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["write","delete"]},"name":{"type":"string"},"type":{"type":"string","enum":["user","directive","project","reference",""]},"description":{"type":"string"},"body":{"type":"string"},"reason":{"type":"string"}},"required":["op","name","type","description","body","reason"],"additionalProperties":false}},"summary":{"type":"string"}},"required":["ops","summary"],"additionalProperties":false}`
