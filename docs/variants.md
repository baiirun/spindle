# Context variants for picking work back up

What a fresh or recycled project coordinator is handed so it can continue a long-running thread, every variant
tried so far, what each scored, and the design we are moving to. Scores and raw tables are in `results.md`; this
page is the map. Last updated 2026-09-28.

## The question

One long-running coordinator per project. When it compacts, or when a fresh coordinator replaces it, what context
lets it (a) pick the work back up after a gap and (b) answer questions about earlier decisions, including ones that
changed? Cross-session discovery is out of scope.

## Test sets

| Set | Thread | Kind | Cases |
|---|---|---|---|
| `eldspire-probes-v1` | TTRPG log (Codex `019e85fe`, Jun 2 – Aug 7, 48 compactions) | recall: changed, current, why, tried, rejected; median fact age 26.5 days | 30 |
| `eldspire-pickups-v1` | same | pick-ups after 5–50 h gaps, the user's real opener | 10 |
| `pickups-other-v1` (chimi) | chimi, a programming-language build thread | pick-ups after 4–93 h gaps; openers like "go", "keep going", "what next" | 10 |
| `pickups-other-v1` (aetherflow) | aetherflow, a coding thread | same | 10 (not yet run) |

All in `data/eval/` (gitignored). One model reads, writes summaries and grades in every arm (`internal/llm/llm.go`).

## Building blocks

Every variant is a mix of these.

| Block | What it is | Where |
|---|---|---|
| **User messages** | Every user message before the cutoff, verbatim. What Codex keeps at compaction (up to 64k tokens). | `ArmUser`, default in user-* arms |
| **Recent tail** | Both sides, verbatim, since the real thread's last compaction. What every agent keeps. | `--tail`, `ArmTail` |
| **Fresh summary** | One summary written at the last compaction from the preceding ~150k characters, with Droid 0.228's adaptive prompt. Written once, not chained. | `--fresh-summary 150000` |
| **Chained summary** | A summary rewritten at each of the 48 real compaction points from the previous summary plus the conversation since. Prompts: Codex fallback, Claude-style 9 sections, spindle working state, Droid adaptive. | `spin eval handoffs --format codex\|claude\|state\|droid` |
| **Memory v1 / v2** | `spin dream` folds episodes into a project memory, rewritten whole at each step. v2 adds "current rules and decisions", max 6 threads, a code-built history index. | `internal/dream` |
| **Memory v3** | Same fold, but the model emits add / replace / drop edits and code applies them, each citing its source. Sections: current rules and decisions (value, why, who decided, what it replaced), active threads, recently changed, history. | `internal/dream/ops.go` |
| **Episodes / raw** | Searchable episode summaries of ~40k-character chunks, or the raw transcript, via `spin` tools. | `ArmEpisodes`, `ArmRaw` |
| **Native** | Reopen the real Codex thread cut at the cutoff, with its own compaction (user messages + encrypted summary + tail), no tools. The benchmark. | `ArmNative` |

## Variants tested

Scores are 0–1 against hand-checked checklists, one run each unless noted. With 10–30 cases, gaps under about
0.1 are noise.

| Variant | Eldspire recall (30) | Eldspire pick-ups (10) | chimi pick-ups (10) | Verdict |
|---|---|---|---|---|
| Native Codex | 0.65 | 0.51 | 0.96 | benchmark |
| Raw transcript search | 0.25 | | | reports old versions as current |
| Episodes search | 0.27 | | | same |
| Memory v2 alone | 0.57 | | | tracks current rules, no reasons |
| Episodes + memory v2 | 0.47 | | | agent trusts old evidence over memory |
| User messages only | 0.61 (2 runs) | 0.57 | 0.21 | carries design threads, empty on build threads |
| User + memory v2 | 0.64 (2) | | | noise |
| User + chained Codex handoff | 0.61 (2) | | | noise |
| User + chained Claude-style summary | 0.61 (2) | | | chained rewrites drop old state |
| User + chained working-state snapshot | 0.64 (2) | | | collapsed to 367 characters at one step |
| User + memory v3 | 0.67 (2) | 0.44 | 0.30 | best Eldspire recall; stale rule hurt pick-ups |
| Last 40 user messages + v3 | 0.33 | | | a message window can't replace the history |
| Chained Droid summary + tail (Droid's shape) | 0.18 | 0.25 | | no user history, old facts lost |
| User + chained Droid summary | 0.62 | 0.56 | | same as user only |
| Tail only | | | 0.82 | misses pick-ups right after a compaction |
| User + tail | | | 0.82 | |
| Tail + v3 | | | 0.88 | v3 lags the live thread (4.5 h on chimi) |
| **Tail + fresh summary** | 0.21 | 0.31 | **0.95** | matches native on build threads at 23k tokens |
| Tail + fresh summary + v3 | | | 0.83 | extra stale context hurts |
| **User + tail + fresh summary** | **0.66** | **0.55** | 0.84 | about native on both threads; current best |

## What we learned

1. **The user's verbatim messages carry old facts and reasons.** On the design thread they alone match native,
   including "why" questions. On the build thread they're nearly empty ("go", "keep going").
2. **The recent tail carries pick-ups on build threads.** The state lives in the agent's last turns.
3. **A summary written fresh at the last compaction covers "where we just were"**, the gap the tail leaves right
   after a compaction. It closed chimi's gap to native (0.82 → 0.95).
4. **Rewritten summaries lose old state.** Every chained variant, whatever the prompt, dropped older rules. A single
   fresh summary also can't hold old facts (Eldspire 0.21 without user messages). The Droid-shaped collapse is
   missing user history, not the model or the prompt.
5. **Code-applied edits stop silent loss but not stale entries.** v3 grew steadily with no collapse, but kept a
   Shadowdark damage-dice rule after it was dropped, because the model never emitted the replace.
6. **Decisions alone aren't the product.** A compaction summary is a continuation prompt; decisions are one part of it.

## How other agents compact (checked in source or 2026 docs)

- **Codex (remote):** keeps every user message verbatim up to 64k tokens, drops assistant turns, adds one encrypted
  summary of the agent's side. Local fallback keeps 20k tokens plus a readable handoff prompt.
- **Claude Code:** a 9-section summary (intent, concepts, files, errors and fixes, problem solving, all user
  messages, pending tasks, current work, next step quoting the latest exchange), a pointer to the transcript, and
  recent files re-read. A newer mode keeps the last 10k–40k tokens verbatim.
- **Factory Droid:** keeps a ~40k-token verbatim tail and one running summary updated by merging in each new span;
  task list and loaded skills kept separately. 0.228's flagged adaptive prompt is the one our fresh summary uses.

None of them track what replaced what, or why a decision changed.

## Proposed design (agreed with Byron 2026-09-28, not yet tested)

What a picked-up coordinator gets, in order:

1. **User messages, verbatim.** All while they fit; search for older ones beyond a budget.
2. **Continuation summary**, in two halves:
   - **Now** (rewritten fresh at every compaction from the recent stretch):
     - the latest ask and the last exchange, verbatim
     - where the work stands: done, in flight, which files
     - the exact next step
     - open questions and what we're waiting on
   - **Ledger** (carried forward from the previous summary and only edited: add, replace, drop, applied by code):
     - decisions in force: what, why, who decided (with the user's words when they approved a proposal), what it
       replaced
     - tried or rejected, and why
     - standing preferences and corrections

   Every line cites where it came from in the conversation. Keep the adaptive prompt's rules: separate user
   decisions from assistant proposals, don't turn proposals into commitments, point to files instead of copying.
3. **Recent tail**, both sides, verbatim, ~20–40k tokens.
4. **Pointers** to the full transcript and files, for looking things up instead of trusting the summary.

Why the split: fresh rewrites are good at the current state and bad at keeping old things; code-applied edits are
the opposite. The ledger is v3 narrowed to one part of the summary.

Open risks: the ledger can still keep a replaced rule if the model misses the change; user messages grow without
bound, so search over older ones is eventually needed.

## In flight / next

- Running: fresh summaries cached so reruns reuse them; user + tail + fresh summary 2–3 times on chimi and Eldspire;
  tail + fresh summary 2–3 times on chimi (is 0.95 vs 0.84 real?); aetherflow's 10 pick-ups; user + tail + v3 on
  Eldspire as the fair comparison against the fresh summary.
- Next: build the two-half continuation summary and test it against user + tail + fresh summary on chimi and Eldspire.
