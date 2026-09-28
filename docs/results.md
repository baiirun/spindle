# Results

Aggregate scores only. Per-trial briefs, traces and grades live in `runs/` (gitignored).

## 2026-09-28 — two-part continuation summary

**Why.** `variants.md` proposed splitting the compaction summary in two: a **Now** part rewritten fresh at every
compaction (fresh rewrites are good at the current state) and a **Ledger** carried forward and changed only by edits
that code applies (code-applied edits don't silently lose old state). This round tests it against the current best.

**What was built** (`internal/eval/twopart.go`, `spin eval two-part`, `spin eval trials --two-part`):
- **Now**, written at each real compaction from the preceding 150k characters (the fresh-summary window): the latest
  user message and the assistant's last reply, copied verbatim by code; where the work stands; the exact next step;
  open questions and what we're waiting on. Every line cites item IDs. Budget 6k characters (actual 1.1–3.6k). Droid's
  adaptive-prompt rules are kept. The model sometimes returned a cut-off Now part; the builder retries until all
  sections are present.
- **Ledger**, chained across every real compaction up to each case's cutoff: the model returns add / replace / drop
  edits (memory v3's mechanism) for decisions in force (what, why, who decided with the user's approving words, what
  it replaced), tried or rejected, and standing preferences. Code applies them, drops citations that aren't in the
  conversation, and records the old text on replace. Budget: the model is told 16k characters; above 20k, code evicts
  the least recently touched entries.
- Both parts are cached (`data/eval/two-part-cache`, gitignored), so every run reads the same summary. They go in the
  same slot as the fresh summary, so the only change from the current best is the summary.

**Setups.** New: user messages + recent tail + two-part summary. Current best: user messages + recent tail + fresh
summary (the three cached runs from the previous round, reused). 3 runs each; mean of per-run means, range across runs.
"Paired" is the new setup minus the current best on the same cases, averaged over runs (± standard error).

| Thread | Setup | Mean | Range | Pitfalls repeated/run | Tokens | Paired |
|---|---|---|---|---|---|---|
| chimi pick-ups (10) | **two-part** | **0.93** | 0.88–0.95 | 0 | 40k | +0.05 ± 0.06 (6 better, 2 worse) |
| | fresh summary | 0.87 | 0.85–0.89 | 0 | 38k | |
| aetherflow pick-ups (10) | **two-part** | **0.71** | 0.69–0.76 | 0 | 35k | +0.06 ± 0.06 (6 better, 2 worse) |
| | fresh summary | 0.65 | 0.53–0.79 | 0 | 31k | |
| Eldspire pick-ups (10) | **two-part** | **0.55** | 0.49–0.60 | 1.0 | 104k | +0.07 ± 0.05 (6 better, 3 worse) |
| | fresh summary | 0.48 | 0.45–0.50 | 0.3 | 100k | |
| Eldspire recall (30) | two-part | 0.67 | 0.63–0.71 | 2.7 | 125k | +0.03 ± 0.03 (11 better, 13 worse) |
| | fresh summary | 0.64 | 0.63–0.65 | 1.7 | 120k | |

Native Codex for reference (1 run each): chimi 0.96, aetherflow 0.58, Eldspire pick-ups 0.51, recall 0.65.

**Ledger across compactions.**

| Thread | Compactions | Entries (early → end) | Characters at end | Replaced | Dropped by the model | Evicted by the cap |
|---|---|---|---|---|---|---|
| chimi | 45 | 6 at step 2 → 35 by step 11, then ~32 | 17.4k | 21 | 1 | 53 |
| aetherflow | 30 | 3 → 41 | 17.9k | 13 | 1 | 3 |
| Eldspire | 48 | 9 at step 1 → 38 by step 13, then ~31–41 | 17.7k | 20 | 0 | 111 |

**Reading.**
- **The two-part summary is ahead on every set, by 0.03–0.07.** Each gap is about one standard error, so no single
  set settles it, but all four point the same way, the chimi and aetherflow runs are steadier than before, and the
  cost is 2–5k more tokens. Eldspire pick-ups (0.55) is now above native's single run (0.51); chimi (0.93) is close
  to native's 0.96.
- **The ledger is not doing what it was designed to do.** It reached its cap by compaction 9 on chimi and 13 on
  Eldspire, and from then on code eviction, not the model, decided what stayed. The model almost never drops an entry
  (2 drops in 123 compactions). By the end of Eldspire every entry was from Aug 4 or later: the ledger had become a
  "recent decisions" list, and old decisions survive only through the user's messages. On chimi it filled with
  progress entries ("M4 progress: …", "M2 is complete…") that belong in Now, and these pushed out core language
  decisions (effects and dependencies, expression syntax, the function execution model).
- **So the gain probably comes from the Now part and from recent decisions**, not from long-term tracking. The
  Now part names the next step and the waiting-on items explicitly and quotes the latest exchange, which the adaptive
  summary sometimes left implicit.
- **Stale entries.** None of the obvious kind: the Shadowdark damage-dice rule that tripped memory v3 is correctly
  recorded as dropped ("drop player damage dice in favor of fictional positioning") until evicted. Eldspire pick-ups
  repeated slightly more pitfalls (1.0 vs 0.3 per run), as did recall (2.7 vs 1.7); with 10–30 cases this is a few
  answers, but it's the same direction v3 showed and is worth reading case by case before building on the ledger.
  Some entries are misfiled: an open question ("the Wayfinder destination is not yet specified") and assistant-only
  choices for a design exercise sit under "decisions in force".
- Fix before relying on the ledger: keep progress out of it (reject entries about milestones or status), give
  evictions to the model as an explicit "merge or drop" step instead of silent least-recently-used eviction, and
  measure what a Now-only summary scores, to separate the two parts' contributions.

## 2026-09-28 — repeat runs with cached summaries, and aetherflow

**Why.** The last round was one run per case, and the fresh summary was regenerated each run, so gaps like chimi's
0.95 vs 0.84 could be noise. Fresh summaries are now cached per session, compaction and window size
(`data/eval/fresh-summary-cache`, gitignored), so every run of a case reads the same summary. Each setup below was
run 3 times (memory v3 and native aetherflow: 2 and 1). Scores are the mean of per-run means; range is the lowest
and highest run. Runs that hit the Codex usage limit were completed by rerunning only the failed cases.

| Thread | Setup | Runs | Mean | Range | Pitfalls repeated/run | Tokens |
|---|---|---|---|---|---|---|
| chimi pick-ups (10) | user + tail + fresh summary | 3 | **0.87** | 0.85–0.89 | 0 | 38k |
| | tail + fresh summary | 3 | 0.84 | 0.77–0.90 | 0 | 23k |
| | native (earlier, 1 run) | 1 | 0.96 | | | 104k |
| aetherflow pick-ups (10) | user + tail + fresh summary | 3 | 0.65 | 0.53–0.79 | 0 | 31k |
| | tail + fresh summary | 3 | **0.70** | 0.61–0.78 | 0 | 21k |
| | native | 1 | 0.58 | | 0 | 151k |
| Eldspire pick-ups (10) | user + tail + fresh summary | 3 | 0.48 | 0.45–0.50 | 0.3 | 100k |
| | user + tail + memory v3 | 2 | 0.49 | 0.47–0.50 | 1.0 | 110k |
| | native (earlier, 1 run) | 1 | 0.51 | | | |
| Eldspire recall (30) | user + tail + fresh summary | 3 | 0.64 | 0.63–0.65 | 1.7 | 120k |
| | user + tail + memory v3 | 2 | 0.65 | 0.64–0.65 | 2.0 | 132k |
| | native (earlier, 1 run) | 1 | 0.65 | | 3 | 165k |

**Reading.**
- **Chimi's 0.95 was luck.** Tail + fresh summary averages 0.84 over three runs (0.77–0.90), level with the full
  recipe's 0.87, which is also steadier. Native's single 0.96 is still ahead by about 0.1.
- **Aetherflow agrees with chimi.** On a coding thread, tail + fresh summary (0.70) and the full recipe (0.65) both
  sit above native's one run (0.58), at a fifth of the tokens. Run-to-run spread here is large (0.53–0.79), so the
  order among the three is not settled.
- **On Eldspire the fresh summary and memory v3 are the same.** 0.48 vs 0.49 on pick-ups and 0.64 vs 0.65 on recall,
  both level with native. v3 repeats a few more stale answers (1.0 vs 0.3 per run on pick-ups) and costs 10k more
  tokens, so the fresh summary is the cheaper choice for the same score.
- **Eldspire reruns are stable** (spread 0.02–0.05); chimi and aetherflow vary 0.1–0.25 between runs of the same
  cases with the same summary. On build threads, single runs can't separate setups within about 0.15.
- The full recipe (user + tail + fresh summary) is about native on all three threads. Nothing tested beats native
  clearly. Next (not started): the two-part continuation summary in `variants.md`.

## 2026-09-28 — recent tail + fresh summary, and a second thread (chimi)

**Why.** Everything before this was one design-brainstorm thread. To check generality, 10 honest pick-ups were built
on chimi (a programming-language build thread; openers like "what next", "keep going", "go") in
`data/eval/pickups-other-v1.jsonl` (10 aetherflow cases are built but not run).

**New arm options** (`internal/eval/userarm.go`):
- `--tail`: both sides of the conversation, verbatim, since the real thread's last compaction, which is what a
  compacting agent still sees. Arm `tail` gives only this.
- `--fresh-summary N`: one handoff summary written at that last compaction from the preceding N characters (150k),
  with Droid's adaptive prompt. It is written once and not chained.
- `--no-user-history`: omit the user-message history.

**Chimi pick-ups (10, 1 sample):**

| Arm | Score | Tokens |
|---|---|---|
| native | 0.96 | 104k |
| **tail + fresh summary** | **0.95** | 23k |
| tail + v3 memory | 0.88 | 26k |
| user + tail + fresh summary | 0.84 | 40k |
| tail + fresh summary + v3 | 0.83 | 27k |
| tail only | 0.82 | 22k |
| user + tail | 0.82 | 36k |
| user + v3 (no tail) | 0.30 | 37k |
| user only | 0.21 | 33k |

**Eldspire with the same options:**

| Arm | Recall probes (30) | Pick-ups (10) |
|---|---|---|
| native | 0.65 | 0.51 |
| user + tail + fresh summary | **0.66** (1 pitfall repeated, the lowest) | 0.55 |
| user + memory v3 | 0.67 | 0.44 |
| tail + fresh summary | 0.21 | 0.31 |

**Reading.**
- **User-messages-as-base was an Eldspire artifact.** On chimi the user's words alone score 0.21; the state lives in the
  agent's turns.
- **The recent tail carries build-thread pick-ups** (0.82 alone). The gap to native comes from pick-ups right after a
  compaction (a compaction 3 minutes before the question), and a fresh summary written at that compaction closes it (0.95).
- **Old facts and rationale need the user's messages.** On Eldspire, tail + fresh summary alone collapses (0.21);
  adding the user's messages brings it level with native (0.66 vs 0.65) with the fewest stale answers.
- **No single arm is best on both yet.** user + tail + fresh summary is within noise of native on both threads (0.84
  chimi, 0.66/0.55 Eldspire). The fresh summary is regenerated per run, which adds variance; one sample, 10–30 cases.
- **v3 on top of tail + summary hurts** (0.83 on chimi). Extra, partly stale context pulls answers off course.
- Design implied: recent verbatim tail + a fresh summary at each compaction (no chain) + the user's message history.
  Next: rerun the combined arm with fixed, cached summaries and 2–3 samples on both threads, and run aetherflow.

## 2026-09-28 — pick-up continuation cases, and Factory Droid's compaction

**Pick-up set** (`data/eval/eldspire-pickups-v1.jsonl`, gitignored): 10 real moments in the TTRPG thread where the
user resumed design work after a 5–50 hour gap. The opener is the user's verbatim message and the agent writes a handoff
brief. The checklist says what the continuation needed (3–5 items, cited before the cutoff); the user's later
corrections are pitfalls. OSC 4, Eldspire 6; Jul 10 to Aug 6.

**Droid arms.** `spin eval handoffs --format droid` chains Factory Droid 0.228's adaptive compaction prompt (feature
flag `AdaptiveCompactionPrompt`, taken from the binary) at the thread's 48 compaction points. `droid` is that summary
plus the verbatim turns since it, with no separate user-message history, which is Droid's shape; `user + droid` adds all
user messages. The v3 memory was rebuilt with steps cut at every pick-up and probe time.

| Arm | Pick-ups (10), 1 sample | Recall probes (30) |
|---|---|---|
| native | 0.51 | 0.65 (1 sample) |
| user | **0.57** | 0.61 (2) |
| user + memory v3 | 0.44 | **0.67** (2; runs 0.69, 0.66) |
| droid-style | 0.25 | 0.18 (1) |
| user + droid summary | 0.56 | 0.62 (1) |

Paired, pick-ups: user − native +0.07 (SE 0.07); user + v3 − user −0.13 (SE 0.06); droid-style − native −0.26 (SE 0.07).
Probes: user + v3 − native +0.02 (SE 0.04); droid-style − native −0.47 (SE 0.06).

**Reading.**
- **The user's verbatim messages are the workhorse.** They match or beat native on both sets with 25–30% fewer tokens.
- **Summary-only memory collapses.** The chained adaptive summaries (1.4k–10k characters, swinging from step to step)
  lose most old state: 0.25 and 0.18. This uses our reader model at low effort; Droid in production uses stronger models
  and a ~40k-token recent tail, so treat this as a lower bound. The direction matches the snapshot collapse.
- **Memory v3 helps recall but hurts pick-ups** (−0.13, about 2 SE). In k-007 it asserted Shadowdark damage dice,
  which were dropped on Jul 24: the fold kept a stale rule because the model never emitted the supersede edit.
  Code-applied edits prevent silent loss, not stale entries. Its 0.69 recall edge over native is within noise after the
  second sample (0.67 average).
- Next: audit v3's current-rules section for stale entries against the probe gold, and make supersession explicit in
  the fold.

## 2026-09-27 — the user's own words: what native Codex actually remembers

**Finding.** Codex compaction (`type: compacted` in the rollout) replaces history with **every user message
verbatim** (1,947 messages, about 230k characters at the last compaction), one encrypted `compaction` item of about 7k
characters, and the app's developer instructions. The kept history grows at each of the thread's 48 compactions; user
messages are never dropped. Only the assistant's side is summarized.

**Arms** (same 30 probes; no tools; `internal/eval/userarm.go`):
- `user`: all of the user's messages before the cutoff, verbatim.
- `user-memory`: the same plus the memory v2 snapshot, which the prompt marks as authoritative for current rules.
- `user-handoff`: the same plus a chained handoff summary made with Codex CLI's public fallback compaction prompt
  at each real compaction point (`spin eval handoffs`; the summaries are about 2k characters each), plus the verbatim
  turns since the last compaction.

| Arm | Samples | All | Changed | Current | Why | Tried | Rejected | Contradicted | Pitfalls/sample | Tokens |
|---|---|---|---|---|---|---|---|---|---|---|
| native | 1 | 0.65 | 0.51 | 0.67 | 0.86 | 0.62 | 0.66 | 3% | 3 | 165k |
| user | 2 | 0.61 | 0.50 | 0.45 | 0.85 | 0.73 | 0.65 | 10% | 5 | 113k |
| user-memory | 2 | 0.64 | 0.54 | 0.71 | 0.80 | 0.61 | 0.50 | 12% | 4.5 | 126k |
| user-handoff | 2 | 0.61 | 0.56 | 0.59 | 0.71 | 0.58 | 0.62 | 7% | 2 | 126k |

Paired against native: user −0.04 (SE 0.05), user-memory −0.02 (SE 0.04), user-handoff −0.05 (SE 0.04).
Against user: memory +0.03, handoff 0.00.

**Reading.**
- **Native's recall is almost entirely the user's own words.** The user's messages alone match native within noise,
  including "why" (0.85 vs 0.86), with 30% fewer tokens.
- **Neither summary adds much on average.** The memory lifts "current" (0.45 → 0.71) but costs "rejected"; the
  handoff summary halves repeated pitfalls. Both are within noise overall.
- **Currency is unsolved everywhere** (0.50–0.56 on "changed"). The misses are agent proposals the user accepted with
  "ok let's try it": the user's side alone can't say what was accepted, and the summaries blur it. Memory v2 alone
  (0.61 on "changed") is still the best at this.

**Follow-up: snapshots written right at the cutoff** (chained through the 48 compaction points plus the 6 cutoffs,
one second before each question; 2 samples each):

| Arm | All | Changed | Current | Why | Tried | Rejected | Pitfalls/sample |
|---|---|---|---|---|---|---|---|
| user + Claude-style compaction summary | 0.61 | 0.52 | 0.57 | 0.76 | 0.60 | 0.66 | 3 |
| user + spindle working-state snapshot | 0.64 | 0.60 | 0.45 | 0.83 | 0.72 | 0.68 | 2 |

Neither beats the user's messages alone by more than noise (+0.00 and +0.03). **This is not a valid ceiling:** the
chained snapshots forget. Each step is a full model rewrite, and old state falls out. The working-state snapshot
collapsed to 367 characters at one step, and at the Aug 6 cutoff it held only the latest few days of work, with no Push
or Tag Team rules at all. Freshness doesn't help when every rewrite can drop old content. The snapshot needs
code-enforced persistence (the model emits edits, code applies them: the dream v3 direction), not a better prompt.

**Follow-up: dream v3 (edit operations applied by code).** One fold, batch 10 with steps cut at the 6 probe times
(17 steps), 0 unresolved citations. The memory grows steadily from 8k to 26k characters with no collapse. Then user
messages + v3 on the 30 probes, 1 sample:

| Arm | Samples | All | Changed | Current | Why | Tried | Rejected | Pitfalls/sample | Tokens |
|---|---|---|---|---|---|---|---|---|---|
| native | 1 | 0.65 | 0.51 | 0.67 | 0.86 | 0.62 | 0.66 | 3 | 165k |
| user | 2 | 0.61 | 0.50 | 0.45 | 0.85 | 0.73 | 0.65 | 5 | 113k |
| user + memory v2 | 2 | 0.64 | 0.54 | 0.71 | 0.80 | 0.61 | 0.50 | 4.5 | 126k |
| **user + memory v3** | 1 | **0.69** | 0.59 | 0.65 | 0.86 | 0.62 | **0.75** | **2** | 126k |

Paired: v3 − native +0.03 (SE 0.05), v3 − user +0.08 (SE 0.05), v3 − v2 +0.05 (SE 0.05). It is the first arm
above native, but with one sample that is not established. It gains on the accepted-proposal misses: push 0.67 (native
0.17) and tag team 0.50 (native 0).

**Follow-up: only the user's last 40 messages + memory v3** (`--user-last 40`, 1 sample): **0.33** (changed 0.36,
current 0.24, why 0.50, tried 0.33, rejected 0.19; 13% contradicted; 6 pitfalls repeated; 33k tokens). That is −0.35
(SE 0.07) against all messages + v3. A recent window cannot stand in for the user's message history: the fold carries
the current state only when the user's own words are there to anchor and explain it.

**Setup.** A new set of 30 hand-vetted probes (`data/eval/eldspire-probes-v1.jsonl`, gitignored like the rest of
`data/`) on the TTRPG log thread (`019e85fe`). A fresh coordinator starts at one of six late cutoffs (Aug 1 to Aug 7),
and each probe asks about a fact from June or July in the user's terse style. One opener is the user's real message;
the other 29 are vague openers with no topic-leaking rewrite. The gold is the original evidence (cited items, all
before the cutoff) plus a check on whether the fact was later superseded. 19 probes list the stale answer as a
pitfall. Types: changed 9, current 7, why 6, tried already 4, rejected 4. The median age of the first fact is 26.5
days; the median age of the current answer is 10.5 days. Known session in every arm, one sample each, 0 errors. The
memory arms use the existing v2 snapshots, the nearest 0–2.5 days before each cutoff.

| Arm | All (30) | Changed | Current | Why | Tried | Rejected | Contradicted | Pitfalls repeated | Tokens |
|---|---|---|---|---|---|---|---|---|---|
| raw | 0.25 | 0.08 | 0.43 | 0.25 | 0.23 | 0.36 | 13% | 8 | 173k |
| episodes | 0.27 | 0.17 | 0.38 | 0.25 | 0.21 | 0.36 | 23% | 6 | 166k |
| memory v2 alone | 0.57 | 0.61 | 0.69 | 0.33 | 0.67 | 0.50 | 10% | 2 | — |
| episodes + memory v2 | 0.47 | 0.44 | 0.71 | 0.31 | 0.42 | 0.39 | 17% | 7 | 251k |
| native (reopened Codex thread) | **0.65** | 0.51 | 0.67 | **0.86** | 0.62 | 0.66 | **3%** | 3 | 165k |

By the age of the first fact (21 days or less, n=12, vs over 21 days, n=18): raw 0.30 vs 0.22, memory v2 alone 0.70 vs 0.48,
episodes + memory v2 0.60 vs 0.38, native 0.71 vs 0.61.

**Reading.**
- **Search-based arms fail currency.** Raw and episodes find the older discussion first and report it as
  current: OSC's post-roll push as Eldspire's push, Shadowdark damage dice, intermediate wound floors.
- **The fold is what tracks currency.** Memory alone scores 0.61 on "changed" questions, while search scores 0.08–0.17.
- **The agent overrides the fold.** Episodes + memory v2 is 0.10 below memory alone and repeats 7 stale
  answers against 2, because older transcript evidence wins over the snapshot.
- **The memory drops rationale.** On "why" questions, native scores 0.86 and every spindle arm scores 0.25–0.33.
- One sample on 30 probes: gaps under about 0.1–0.15 are noise.

## 2026-09-27 — project memory on the Eldspire TTRPG log (`spin dream`)

**Setup.** One real long-running project: the TTRPG log Codex thread (06-02 → 08-07, 134 chunks, all
slept). `spin dream` folds its episodes in time order, 10 per step, with a snapshot right before each test
moment, so every test sees the memory as of its own time. 28 tests, all verified against pre-cutoff
evidence: 9 continuation trials and 19 recall questions from the same session ("current", "why",
"changed", "rejected"). Known session in every arm; one sample each; 0 errors.

- **v1 format:** working state, history, superseded. It grew to 45k characters (peak 64k), mostly citations
  (~500), 10–18 threads, and an ever-growing History.
- **v2 format:** "Current rules and decisions" with exact values and who decided; at most 6 active threads;
  last 10 changes; 12k cap with one compress retry; history moved to a code-built index of episode titles.
  It still ended at 22k characters (peak 26k): the model doesn't hold the cap.

| Arm | Continuation (9) | Recall (19) | Recall: current (11) | Recall: why (5) | All (28) | Tokens | Contradicted |
|---|---|---|---|---|---|---|---|
| memory v1 alone | 0.42 | 0.23 | 0.21 | 0.12 | 0.30 | — | 1 |
| memory v2 alone | 0.44 | 0.35 | 0.42 | 0.23 | 0.38 | — | 3 |
| raw | 0.86 | 0.60 | 0.50 | 0.77 | 0.68 | 226k | 2 |
| episodes | 0.82 | 0.68 | 0.70 | 0.64 | 0.72 | 153k | 0 |
| episodes + memory v1 | 0.89 | 0.64 | 0.51 | 0.72 | 0.72 | 215k | 4 |
| **episodes + memory v2** | 0.85 | **0.77** | 0.67 | **0.88** | **0.80** | 178k | **0** |

**Default Codex baseline** (native arm: reopen the real Codex thread cut at each test's moment, with its own
compaction): continuation 0.79, recall 0.78 (current 0.79, why 0.64), all 0.78, 66k tokens, 1 contradicted.
Episodes + memory v2 ties it overall (+0.01; 9 up / 13 flat / 6 down): better on continuation and "why",
worse on "current", at ~2.7× the tokens.

Paired: episodes + memory v2 − episodes is **+0.08** (9 up / 13 flat / 6 down, about 2 SE at n = 28).
v2 − v1 with an agent is +0.07; memory alone gains +0.09 from v1 to v2.

**Reading.**
- **v1 memory added nothing.** Its bulk described threads instead of stating rules, and it misled on
  "current rule" questions (0.51 vs 0.70).
- **v2 is the first memory that helps.** It adds +0.08 over episodes, mostly on recall (0.68 → 0.77, and
  "why" questions 0.64 → 0.88), with no wrong statements and 16% more tokens than episodes alone.
- **Memory alone is still a map, not the answer** (0.38). The agent needs episodes and the transcript for
  specifics.
- Folding is slow (~140 s per step, sequential), because every step rewrites the whole memory and the cap
  forces a second call.

**Next.** Make dream emit edit operations applied by code (smaller output, code-enforced cap, no silent
rewrites); repeat samples on the 28 tests to confirm the +0.08; add long-gap cases across sessions.

## 2026-09-26 — fix 1: discovery agents resume the session they find (`ts-1b9c30`)

**Change** (prompt only, `471fb35`). The discovery prompt now says: once you know which session this continues, run `spin resume` on it to get the latest episode and the transcript after it. Episodes matched by search may be older than the current state.

**Runs.** The same 61 vague-opener trials (`v4w`), raw and episodes arms, one sample each, 0 errors; compared against the 09-26 discovery runs.

| Arm | Coverage | Found session | Coverage when found | Resumed the right session | Read the latest episode (when found) | Tokens |
|---|---|---|---|---|---|---|
| raw, before | 0.41 | 66% | 0.55 | — | — | 219k |
| raw, fix 1 | 0.44 | 61% | 0.66 | 36/61 | — | 183k |
| episodes, before | 0.36 | 69% | 0.46 | — | 21/40 | 172k |
| episodes, fix 1 | **0.47** | 62% | **0.66** | 37/61 | **37/38** | **140k** |

Paired per trial:
- episodes, fix 1 − before: **+0.11** (32 up / 13 flat / 16 down), roughly 4 standard errors.
- raw, fix 1 − before: +0.03 (noise).
- In discovery with fix 1, episodes − raw is +0.04, where it was −0.05 before.

**Reading.**
- **Reading the latest state was the episodes arm's navigation problem, and one prompt line fixed it.** Once found, coverage rose from 0.46 to 0.66 with 19% fewer tokens.
- **Finding the session is now the whole bottleneck.** Every agent ran `resume`, but only 36–37 of 61 picked the right session. When it misses, coverage is about 0.1–0.16, near the 0.05 floor.
- Episodes now slightly lead raw in discovery at lower cost, but that's within noise.

**Next.** Discovery retrieval: a working-directory/project filter on `wake`/`search`, recency, and a per-project session index (Codex's "What's in Memory" pattern). Measure the found rate first.

## 2026-09-26 — cold floor, known handle, and discovery from a vague opener

**Setup.** The same 61 verified v4r trials and claims, one sample per arm, 0 errors.
- **Known handle:** the agent is given the prior session (the existing raw, episodes and native runs) plus the labeler's task text.
- **Discovery:** the trial replays a brand-new session. `trials-openers` rewrites the pickup into a vague, topic-only opener ("where were we on the eldspire rules?"); the openers are in `data/eval/openers-v4w.json`. No handle is given, so the agent must find the earlier session with `spin wake`/`search`. `found_prior` records whether any spin call referenced a prior session.
- **Cold:** the same request with no history and no tools, the floor.

| Framing | Arm | Coverage | Trials w/ contradiction | spin calls | Input tokens | Time |
|---|---|---|---|---|---|---|
| Known-handle request | cold | 0.37 | 2% | 0 | 18k | 8 s |
| Known handle | raw | 0.79 | 11% | 8.8 | 187k | 50 s |
| Known handle | episodes | 0.79 | 10% | 5.1 | 165k | 51 s |
| Known handle | native (56 Codex trials, reference) | 0.84 | 5% | 0 | 104k | 24 s |
| Vague opener | cold | 0.05 | 8% | 0 | 18k | 7 s |
| Vague opener | raw (discovery) | 0.41 | 11% | 6.8 | 219k | 49 s |
| Vague opener | episodes (discovery) | 0.36 | 18% | 5.8 | 172k | 43 s |

**Finding the session is the bottleneck.**

| Discovery arm | Found the prior session | Coverage when found | Coverage when missed |
|---|---|---|---|
| raw | 40/61 (66%) | 0.55 | 0.15 |
| episodes | 42/61 (69%) | 0.46 | 0.14 |

Paired per trial:
- episodes − raw in discovery: −0.05 (17 up / 20 flat / 24 down).
- Discovery vs known handle: −0.38 for raw, −0.42 for episodes.

**Reading.**
- **The known-handle task text leaks.** A cold agent with no history scores 0.37 from the labeler's task wording alone. What memory adds on top of that is about +0.42, not 0.79. Future known-handle trials should use the real message or a vague opener.
- **Discovery works about two-thirds of the time.** When the agent finds the right session it recovers about half the checklist; when it doesn't, it's near the floor. That one step decides most of the score.
- **Episodes don't help discovery yet.** They find the session about as often, but coverage once found is lower (0.46 vs 0.55) and contradictions are higher (18% vs 11%). The agent seems to stop at the episode summary instead of reading the source.
- **Even when found, discovery trails a known handle** (0.55 vs 0.79). A vague opener doesn't tell the agent which part of a long session matters.

**Next, in the order the failures point to.**
1. **Discovery retrieval.** The agent knows its working directory, but `wake`/`search` can't filter by it, and ranking is lexical over whole chunks. Add a `--cwd`/project filter and recency, and return item-level hits. Measure `found_prior` and coverage on the discovery set.
2. **Episodes that lead to the source.** When found, episodes should point the agent at the relevant raw ranges, so their lower coverage and higher contradiction rate go away.
3. **Clean the known-handle framing.** Rerun known handle with the vague opener plus the handle, so it has the same 0.05 floor.

## 2026-09-25 — three arms on the verified set v4r: raw vs episodes vs native Codex

**Trial set v4r.**
- **Mining:** `trials-mine --wide` flagged 584 of ~9,000 user messages, by cue, as the first message of a session within a week of earlier work in the same directory, or as the first message after a 3-hour pause.
- **Classify and label:** 151 were classified as real pickups. The labeler was fixed to see ~30k characters before each pickup and at most 12 items / 45 minutes after it; 149 were labeled.
- **Split and verify:** 1,357 claims came out of the split, and 68 trials kept at least 3 claims with code-checked pre-cutoff citations.
- **Deduplicate:** keeping one trial per session per hour left **61 trials and 332 claims (286 must-know)**. The identifier leak check flags 4 of 332 claims, down from 24 of 192 in v2.

**Arms.** Same task prompt, reader (gpt-6-luna, low effort), judge and claims in every arm; one sample each.
- **raw:** spin over transcripts before the cutoff.
- **episodes:** raw plus episodes projected before the cutoff.
- **native:** `codex exec resume` on a copy of the real Codex thread, cut at the trial's moment and held in a private CODEX_HOME. This is "just keep going in the same thread", including Codex's encrypted compaction summaries. It covers the 56 trials with a Codex prior.

After rerunning 9 trials whose calls died while the Mac slept, the runs have 0 errors and 0 leak suspects.

| Arm (56 Codex-prior trials) | Coverage | Must-know coverage | Trials w/ contradiction | spin calls | Input tokens | Time |
|---|---|---|---|---|---|---|
| raw | 0.80 | 0.82 | 11% | 8.8 | 189k | 51 s |
| episodes | 0.80 | 0.81 | 9% | 5.1 | 165k | 52 s |
| native | **0.84** | **0.84** | **5%** | 0 | **104k** | **24 s** |

**Paired, per trial (single samples; per-trial noise is about ±0.18, and the standard error of a 56-trial mean difference is about 0.027):**

| Comparison | Mean Δ | Up / flat / down |
|---|---|---|
| episodes − raw | −0.01 | 20 / 19 / 22 |
| native − raw | +0.04 | 20 / 23 / 13 |
| native − episodes | +0.04 | 23 / 20 / 13 |

**Slice that matters.**

| Slice | Trials | raw | episodes | native |
|---|---|---|---|---|
| Within-session pickups | 51 | 0.82 | 0.81 | 0.86 |
| New-session pickups | 5 | 0.65 | 0.65 | 0.62 |

In new-session pickups, native resumes the *previous* thread.

**Reading.**
- **All three arms recover about 80% of verified must-know claims.** Native is slightly ahead (+0.04, about 1.5 SE, so not established) and contradicts itself least.
- **Native is clearly the cheapest:** about half the input tokens and half the time of the spin arms. Just continuing the thread is hard to beat on cost.
- **Episodes still don't raise coverage over raw.** They cut lookups by 42% and tokens by 12%.
- **The set is dominated by within-session pickups (51 of 56),** where the live thread has an obvious advantage. Spindle's reason to exist is the other case: new sessions, lost sessions, other harnesses. Only 5 trials test it, and there all arms drop to about 0.63.

**Next.**
1. Mine new-session and cross-harness pickups specifically. The labeler mostly picked the current session as the prior; the target is 30 or more trials where the context lives in a *different* session.
2. Repeat samples on the new-session slice before claiming anything there.
3. Only then hill-climb episodes (`ts-1b9c30`), measured on that slice against native.

## 2026-09-25 — noise, answer-key audit, and rescore (`ts-7c6631`)

**What changed since the baseline.**
- **Trial prompt.** The agent's only job is now to write the handoff brief it would need. Before, it was asked to "continue" while being forbidden to edit, and about 40% of briefs stopped to say they couldn't. That's now 6 of 57 raw briefs and 3 of 19 episode briefs.
- **v2 answer key.** `spin eval trials-split` splits compound checklist items into single claims: 91 items became 192 claims. Lists that belong to one claim stay together.
- **Label audit.** I hand-checked 4 trials against the transcript. **30 of their 45 claims were not true yet at the cutoff.** The labeler had seen 6 items before the pickup and 30 after it, so it often described a later moment of the same long session. t-013 and t-020 were wholly invalid, and t-019 was mostly invalid.
- **v3 answer key.** `spin eval trials-verify` re-grounds every claim in the transcript before the cutoff. The model must cite an item ID, and code keeps a claim only if that item predates the cutoff (see `verify_test.go`). Result: 124 of 192 claims were unsupported and 39 only restated the task. 29 claims were kept and 15 grounded ones added. **9 of 19 trials keep at least 3 claims.** On the 4 audited trials, it agrees with the hand audit.

**Runs.** The v2 key was scored with raw ×3 samples and episodes ×1 (`runs/*-trials-{raw,episodes}-v2-s*`): 76 trial runs, 0 errors, 0 leak suspects. The v3 numbers rescore those same runs, using the v2 grades of the 26 v3 claims that carried over from v2. The 15 added claims have no grades yet.

**Noise, measured on raw.**

| Key | Trials | Mean of 3 raw runs | Spread of run means (sd) | Per-trial sd (median) | Mean \|Δ\| between two runs of the same trial |
|---|---|---|---|---|---|
| v2 (all claims) | 19 | 0.53 | 0.013 | 0.12 | 0.18 |
| v3 (verified claims) | 9 | 0.76 | 0.082 | 0.14 | 0.21 |

A single trial run swings about ±0.18, so per-trial comparisons from one sample are meaningless. On the full 19-trial v2 set, the average is stable (run means 0.52–0.54). On the 9-trial v3 set, with 3–4 claims per trial, it isn't (0.69–0.85).

**Raw vs episodes.**

| Key | Slice | Raw (mean of 3) | Episodes (1 run) | Δ |
|---|---|---|---|---|
| v2 | all 19 | 0.53 | 0.52 | −0.01 |
| v2 | 14 with prior episodes | 0.56 | 0.60 | +0.04 (6 up, 3 flat, 5 down) |
| v3 | 8 with prior episodes | 0.81 | 0.82 | +0.01 |

| Per run | Raw | Episodes |
|---|---|---|
| spin calls | 11.9 | 5.5 |
| Input tokens | 208k | 177k |
| Time | 46 s | 45 s |

**Reading.**
- **Most of the "agents recover only half" gap was the answer key.** On verified claims, both arms recover about 80%.
- **Episodes still show no coverage gain.** They do reach the same recovery with **54% fewer lookups and 15% fewer input tokens**.
- **The clean set is too small to separate arms.** Its run means spread 0.08, and most trials would need many samples. The v2 set is stable but still includes ungrounded claims.

**Next.**
1. Widen trial mining. The original prefilter found only 26 candidates. Loosen it and label with the bounded after-window, then verify. Target: 40 or more trials with at least 3 grounded claims.
2. Score v3 (and its successor) directly, so the added claims get graded.
3. Add the Codex native arm: resume the real thread at the cutoff.

## 2026-09-24 — continuation baseline: raw vs episodes (`ts-7c6631`)

*Superseded in part: this baseline used the v1 answer key, which the 09-25 audit found largely ungrounded, and the old "continue the task" prompt.*

**Setup.** Trial set v1 (`data/eval/continuation-v1.jsonl`, 19 resume trials). Each trial gets a leak-free cutoff snapshot. A fresh gpt-6-luna agent (low effort) may use only `spin` to write a continuation brief. A gpt-6-luna judge (medium effort) grades the brief against the trial's checklist (covered = 1, partial = 0.5) and its pitfalls. One sample per trial per arm, 3 workers.

- **raw:** snapshot has transcript chunks only.
- **episodes:** snapshot also has `episodes-v2` episodes that ended before the cutoff. Priors were projected with `spin sleep --before <cutoff>`.

Runs: `runs/20260923-230647-trials-raw-base`, `runs/20260923-231251-trials-episodes-base`.

| Arm | n | Errors | Coverage | Trials w/ contradiction | Pitfalls repeated | spin calls/trial | Input tokens/trial (mean / median) | Time/trial | Leak suspects |
|---|---|---|---|---|---|---|---|---|---|
| raw | 19 | 0 | 0.49 | 16% | 0 | 12.6 | 196k / 235k | 43 s | 0 |
| episodes | 19 | 0 | 0.54 | 11% | 4 | 7.6 | 206k / 217k | 43 s | 0 |

**The split that matters.** In 5 trials (t-004, t-005, t-011, t-013, t-015) the trial's cutoff falls inside the prior session's first chunk. `--before` only projects chunks that ended before the cutoff, so these trials have no prior episodes and both arms see the same data. They work as a within-arm noise control.

| Slice | n | raw | episodes | Per-trial \|Δ\| |
|---|---|---|---|---|
| Has prior episodes | 14 | 0.53 | 0.58 (6 up, 3 down, 5 tied) | 0.12 mean |
| No prior episodes (control) | 5 | 0.38 | 0.41 | 0.25 mean |

**Reading.**
- **Coverage: no measurable effect yet.** The +0.05 on the 14 trials with episodes is smaller than the run-to-run swing on the control trials, where the same data differed by 0.25 per trial on average. At one sample per trial, n = 19 can't separate them.
- **Efficiency: episodes cut spin calls about 40%** (7.6 vs 12.6 per trial). Input tokens didn't fall with them. The episodes arm had one outlier: t-006 read 658k tokens.
- **Pitfalls and contradictions are noise-dominated.** 3 of the episodes arm's 4 repeated pitfalls, and 4 of its contradicted items, come from t-011, which is a control trial with no episodes.
- **Absolute coverage is about 0.5 in both arms.** Half of what a good continuation needs isn't recovered from either source. That is the bigger gap to work on.

**Next.**
1. Cut noise before hill-climbing. Run 3 samples per trial per arm, and report paired deltas with a spread.
2. Fix the straddling-chunk gap. A prior whose cutoff falls mid-chunk gets no episode, which is common for within-session pickups. Candidates: project the truncated chunk as of the cutoff, or use smaller live windows.
3. Read the low-coverage briefs in both arms (t-009, t-012, t-019, t-024) to see whether misses are retrieval or synthesis failures.
