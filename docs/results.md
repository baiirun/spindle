# Results

Aggregate scores only. Per-trial briefs, traces and grades live in `runs/` (gitignored).

## 2026-09-24 — continuation baseline: raw vs episodes (`ts-7c6631`)

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
