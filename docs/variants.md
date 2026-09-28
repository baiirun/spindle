# Context variants for picking work back up

What a fresh or recycled project coordinator is handed so it can continue a long-running thread, every variant
tried so far, what each scored, and the design we are moving to. Scores and raw tables are in `results.md`; this
page is the map. Last updated 2026-09-28 (decision record v2).

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
| `pickups-other-v1` (aetherflow) | aetherflow, a coding thread | same | 10 |

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
| **Two-part summary** | Now (fresh at each compaction, latest exchange copied verbatim, next step, open questions) + ledger (chained add / replace / drop edits applied by code: decisions, tried or rejected, preferences). Cached. | `spin eval two-part`, `--two-part` |
| **Decision record (searched)** | The v1 ledger with no budget, eviction or clipping, kept out of the prompt; the agent searches it with `spin decisions`. Summary = Now part only. | `--ledger record`, `internal/eval/record.go` |
| **Episodes / raw** | Searchable episode summaries of ~40k-character chunks, or the raw transcript, via `spin` tools. | `ArmEpisodes`, `ArmRaw` |
| **Native** | Reopen the real Codex thread cut at the cutoff, with its own compaction (user messages + encrypted summary + tail), no tools. The benchmark. | `ArmNative` |

## Variants tested

Scores are 0–1 against hand-checked checklists, one run each unless noted; repeat runs give the mean and the range across runs. With 10–30 cases, gaps under about
0.1 are noise.

| Variant | Eldspire recall (30) | Eldspire pick-ups (10) | chimi pick-ups (10) | Verdict |
|---|---|---|---|---|
| Native Codex | 0.65 | 0.51 | 0.96 | benchmark; aetherflow 0.58 |
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
| **Tail + fresh summary** | 0.21 | 0.31 | 0.84 (3 runs, 0.77–0.90) | about native on build threads at ~22k tokens; aetherflow 0.70 (3, 0.61–0.78); the first chimi run's 0.95 was luck |
| Tail + fresh summary + v3 | | | 0.83 | extra stale context hurts |
| **User + tail + fresh summary** | 0.64 (3 runs, 0.63–0.65) | 0.48 (3, 0.45–0.50) | **0.87** (3, 0.85–0.89) | about native on all three threads; aetherflow 0.65 (3, 0.53–0.79); current best |
| User + tail + v3 | 0.65 (2) | 0.49 (2) | | ties the fresh summary on Eldspire, with more stale answers and 10k more tokens |
| **User + tail + two-part summary** | 0.67 (3, 0.63–0.71) | **0.55** (3, 0.49–0.60) | **0.93** (3, 0.88–0.95) | ahead of the fresh summary on all four sets by 0.03–0.07 (each about one standard error); aetherflow 0.71 (3, 0.69–0.76); new best, but the ledger half overflowed (see below) |
| User + tail + Now part only | 0.61 (3, 0.57–0.66) | 0.54 (3) | **0.94** (3, 0.88–0.97) | the pick-up gain is all here; aetherflow 0.71; recall falls without a ledger |
| **User + tail + Now + searchable decision record** | **0.68** (3, 0.65–0.72) | 0.64 (3, 0.52–0.73) | 0.89 (3, 0.85–0.97) | best on Eldspire recall and pick-ups together, fewest stale answers; costs 0.04–0.06 on build threads (aetherflow 0.65) and doubles tokens |
| User + tail + Now + record v2 (approval gate, supersession check) | 0.61 (3, 0.55–0.64) | 0.53 (3, 0.45–0.64) | 0.91 (3, 0.86–1.00) | loses the record's Eldspire gain: the stricter gate drops decisions approved with a short reply; aetherflow 0.67 |
| User + tail + two-part, ledger v2 (consolidating) | 0.62 (3) | **0.65** (3, 0.61–0.71) | 0.87 (3) | best Eldspire pick-ups so far, but loses recall and chimi; aetherflow 0.72 |

## What we learned

1. **The user's verbatim messages carry old facts and reasons.** On the design thread they alone match native,
   including "why" questions. On the build thread they're nearly empty ("go", "keep going").
2. **The recent tail carries pick-ups on build threads.** The state lives in the agent's last turns.
3. **A summary written fresh at the last compaction covers "where we just were"**, the gap the tail leaves right
   after a compaction. It lifts chimi from 0.82 to 0.84–0.87 averaged over three runs (the first run's 0.95 was luck).
4. **Rewritten summaries lose old state.** Every chained variant, whatever the prompt, dropped older rules. A single
   fresh summary also can't hold old facts (Eldspire 0.21 without user messages). The Droid-shaped collapse is
   missing user history, not the model or the prompt.
5. **Code-applied edits stop silent loss but not stale entries.** v3 grew steadily with no collapse, but kept a
   Shadowdark damage-dice rule after it was dropped, because the model never emitted the replace.
6. **Decisions alone aren't the product.** A compaction summary is a continuation prompt; decisions are one part of it.
7. **The two-part summary helps, but mostly through its Now half.** It beat the fresh summary on every set. Its
   ledger hit the size cap by compaction 9–13 and code eviction then decided what stayed: by the end the Eldspire
   ledger held only decisions from the last few days, and chimi's filled with progress notes that pushed out core
   language decisions. The model almost never drops entries itself (2 drops in 123 compactions).
8. **The two halves do different jobs.** The Now part alone gives the whole pick-up gain; the long, specific v1 ledger
   gives recall (+0.06 on Eldspire). A consolidating ledger (v2) that merges down to 10–16 entries helped Eldspire
   pick-ups most (0.65) but lost recall and chimi detail.

9. **A searched record gives the long ledger's recall without crowding the prompt.** Out of the prompt and never
   trimmed, the record matched the v1 ledger on Eldspire recall (0.68) and lifted Eldspire pick-ups to 0.64, but
   cost a little on build threads, where the needed state is recent. The model adds entries and almost never
   replaces or drops them (7 replaces in 123 compactions), so stale entries and misfiled proposals accumulate.

10. **A supersession check makes the model revise the record, but a strict approval gate costs more than it saves.**
    Showing each new decision the same-topic entries got 15 replacements and 63 narrowings. But asking for the
    user's words made the model skip decisions approved with a short reply ("agree"), so the newest version of a
    rule was often missing and the old one stayed. Eldspire recall fell from 0.68 to 0.61.

## How other agents compact (checked in source or 2026 docs)

- **Codex (remote):** keeps every user message verbatim up to 64k tokens, drops assistant turns, adds one encrypted
  summary of the agent's side. Local fallback keeps 20k tokens plus a readable handoff prompt.
- **Claude Code:** a 9-section summary (intent, concepts, files, errors and fixes, problem solving, all user
  messages, pending tasks, current work, next step quoting the latest exchange), a pointer to the transcript, and
  recent files re-read. A newer mode keeps the last 10k–40k tokens verbatim.
- **Factory Droid:** keeps a ~40k-token verbatim tail and one running summary updated by merging in each new span;
  task list and loaded skills kept separately. 0.228's flagged adaptive prompt is the one our fresh summary uses.

None of them track what replaced what, or why a decision changed.

## Proposed design (agreed with Byron 2026-09-28; first test in `results.md`)

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

Tested 2026-09-28: ahead of the fresh summary on all three threads (chimi 0.93, aetherflow 0.71, Eldspire pick-ups
0.55, recall 0.67). But the ledger overflowed its 16–20k budget early on every long thread, so it behaved as a
recent-decisions list, not a record of everything decided. Known fixes: keep progress and open questions out of the
ledger, and replace silent eviction with an explicit merge-or-drop step.

## In flight / next

- Done 2026-09-28: two-part summary (ledger v1), Now only, and ledger v2, 3 runs each; see `results.md`.
- Decided 2026-09-28 (Byron): the ledger is for recall, so it's a long, searchable record out of the prompt, and
  the Now part is the whole summary. Tested the same day; see `results.md`.
- Done 2026-09-28 (Byron chose "Fix it"): record v2 with an approval gate and a supersession check. It revises old
  entries now, but lost the Eldspire gain by recording fewer approved decisions; see `results.md`.
- Open, not started: keep the supersession check and loosen the gate so short approvals of a specific proposal
  still count; build threads still lose a little with search; tokens double. Needs Byron's go-ahead.
