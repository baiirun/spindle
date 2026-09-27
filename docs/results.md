# Results

Aggregate scores only. Per-trial briefs, traces and grades live in `runs/` (gitignored).

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
