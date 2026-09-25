package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"spindle/internal/eval"
	"spindle/internal/llm"
)

func runEval(args []string) error {
	if len(args) < 1 {
		return errors.New("eval: want mine | label | run | report | continue | trials | trials-mine | trials-label | trials-split | trials-verify")
	}
	switch args[0] {
	case "trials":
		return evalTrials(args[1:])
	case "trials-mine":
		return evalTrialsMine(args[1:])
	case "trials-verify":
		return evalTrialsVerify(args[1:])
	case "trials-split":
		return evalTrialsSplit(args[1:])
	case "trials-label":
		return evalTrialsLabel(args[1:])
	case "mine":
		return evalMine(args[1:])
	case "label":
		return evalLabel(args[1:])
	case "run":
		return evalRun(args[1:])
	case "report":
		return evalReport(args[1:])
	case "continue":
		return evalContinue(args[1:])
	}
	return fmt.Errorf("eval: unknown subcommand %q", args[0])
}

func evalContinue(args []string) error {
	fs := flag.NewFlagSet("eval continue", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	episodeRoot := fs.String("episodes", roots.Episodes, "episode root")
	source := fs.String("source", "", "source name, e.g. codex")
	session := fs.String("session", "", "source session ID")
	task := fs.String("task", "", "continuation task for the fresh agent")
	runDir := fs.String("out", "", "run output directory")
	fs.Parse(args)
	if *source == "" || *session == "" || *task == "" {
		return fmt.Errorf("eval continue requires --source, --session, and --task")
	}
	if *runDir == "" {
		*runDir = filepath.Join("runs", time.Now().Format("20060102-150405")+"-continuation")
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	result, err := eval.RunContinuation(context.Background(), eval.ContinuationOptions{
		CorpusRoot: *corpusRoot, EpisodeRoot: *episodeRoot, Source: *source, Session: *session,
		Task: *task, WorkDir: workDir, RunDir: *runDir,
	})
	if err != nil {
		return err
	}
	fmt.Printf("continuation used resume: %t → %s\n", result.UsedResume, filepath.Join(*runDir, "continuation.json"))
	return nil
}

func evalMine(args []string) error {
	fs := flag.NewFlagSet("eval mine", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	slice := fs.String("slice", "Zaum", "only sessions whose cwd contains this")
	out := fs.String("out", "data/eval/candidates.jsonl", "classified candidates")
	model := fs.String("model", llm.Classifier, "classifier model")
	workers := fs.Int("workers", 6, "parallel calls")
	fs.Parse(args)

	cands, err := eval.MineCandidates(*corpusRoot, *slice)
	if err != nil {
		return err
	}
	fmt.Printf("prefilter: %d candidate messages\n", len(cands))
	kept, cost, err := eval.ClassifyCandidates(context.Background(), cands, *model, *workers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].AskedAt.Before(kept[j].AskedAt) })
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	fmt.Printf("classified as decision recall: %d (cost $%.2f) → %s\n", len(kept), cost, *out)
	return eval.WriteCandidates(*out, kept)
}

func evalLabel(args []string) error {
	fs := flag.NewFlagSet("eval label", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	in := fs.String("in", "data/eval/candidates.jsonl", "classified candidates")
	out := fs.String("out", "data/eval/decisions-v0.jsonl", "labeled eval set")
	workers := fs.Int("workers", 4, "parallel calls")
	cacheDir := fs.String("cache", "data/eval/label-cache", "per-candidate labeler verdicts")
	fs.Parse(args)

	cands, err := eval.ReadCandidates(*in)
	if err != nil {
		return err
	}
	items, cost, err := eval.LabelCandidates(context.Background(), *corpusRoot, *cacheDir, cands, *workers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AskedAt.Before(items[j].AskedAt) })
	byConf := map[string]int{}
	for i := range items {
		items[i].ID = fmt.Sprintf("q-%04d", i+1)
		byConf[items[i].Confidence]++
	}
	fmt.Printf("labeled %d of %d candidates with %s (cost $%.2f): %v → %s\n", len(items), len(cands), llm.Labeler, cost, byConf, *out)
	return eval.WriteItems(*out, items)
}

func evalTrialsMine(args []string) error {
	fs := flag.NewFlagSet("eval trials-mine", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	out := fs.String("out", "data/eval/resume-candidates.jsonl", "classified resume candidates")
	workers := fs.Int("workers", 6, "parallel calls")
	fs.Parse(args)
	cands, err := eval.MineResumeCandidates(*corpusRoot)
	if err != nil {
		return err
	}
	fmt.Printf("prefilter: %d candidate messages\n", len(cands))
	kept, err := eval.ClassifyResumeCandidates(context.Background(), cands, *workers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	fmt.Printf("classified as resumption: %d → %s\n", len(kept), *out)
	return eval.WriteResumeCandidates(*out, kept)
}

func evalTrialsLabel(args []string) error {
	fs := flag.NewFlagSet("eval trials-label", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	in := fs.String("in", "data/eval/resume-candidates.jsonl", "classified resume candidates")
	out := fs.String("out", "data/eval/continuation-v0.jsonl", "labeled trial set")
	cacheDir := fs.String("cache", "data/eval/trial-cache", "per-candidate labeler verdicts")
	workers := fs.Int("workers", 6, "parallel calls")
	fs.Parse(args)
	cands, err := eval.ReadResumeCandidates(*in)
	if err != nil {
		return err
	}
	trials, err := eval.LabelTrials(context.Background(), *corpusRoot, *cacheDir, cands, *workers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	byConf := map[string]int{}
	for _, t := range trials {
		byConf[t.Mode+"/"+t.Confidence]++
	}
	fmt.Printf("labeled %d of %d candidates: %v → %s\n", len(trials), len(cands), byConf, *out)
	return eval.WriteTrials(*out, trials)
}

func evalTrials(args []string) error {
	fs := flag.NewFlagSet("eval trials", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	episodes := fs.String("episodes", roots.Episodes, "episode root")
	in := fs.String("in", "data/eval/continuation-v0.jsonl", "trial set")
	arm := fs.String("arm", eval.ArmEpisodes, "raw | episodes")
	only := fs.String("only", "", "comma-separated trial IDs (default: all)")
	workers := fs.Int("workers", 3, "parallel trials")
	tag := fs.String("tag", "", "label for this run")
	fs.Parse(args)
	if *arm != eval.ArmRaw && *arm != eval.ArmEpisodes {
		return fmt.Errorf("unknown arm %q", *arm)
	}
	trials, err := eval.ReadTrials(*in)
	if err != nil {
		return err
	}
	if *only != "" {
		want := map[string]bool{}
		for _, id := range strings.Split(*only, ",") {
			want[strings.TrimSpace(id)] = true
		}
		var kept []eval.Trial
		for _, t := range trials {
			if want[t.ID] {
				kept = append(kept, t)
			}
		}
		trials = kept
	}
	name := time.Now().Format("20060102-150405") + "-trials-" + *arm
	if *tag != "" {
		name += "-" + *tag
	}
	runDir, err := filepath.Abs(filepath.Join("runs", name))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	repo, _ := os.Getwd()
	bin, err := eval.BuildCLI(context.Background(), repo, runDir)
	if err != nil {
		return err
	}
	results, runErr := eval.RunTrials(context.Background(), eval.TrialRunOptions{
		CorpusRoot: *corpusRoot, EpisodeRoot: *episodes, Version: filepath.Base(*episodes), Binary: bin,
		Arm: *arm, Trials: trials, RunDir: runDir, ScratchDir: filepath.Join("/private/tmp/spindle-trials", name), Workers: *workers,
	})
	s := eval.SummarizeTrials(*arm, results)
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(runDir, "summary.json"), b, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", name, b)
	return runErr
}

func evalTrialsSplit(args []string) error {
	fs := flag.NewFlagSet("eval trials-split", flag.ExitOnError)
	in := fs.String("in", "data/eval/continuation-v1.jsonl", "trial set to split")
	out := fs.String("out", "data/eval/continuation-v2.jsonl", "trial set with one fact per checklist item")
	workers := fs.Int("workers", 4, "parallel calls")
	fs.Parse(args)
	trials, err := eval.ReadTrials(*in)
	if err != nil {
		return err
	}
	split, err := eval.SplitChecklists(context.Background(), trials, *workers)
	if err != nil {
		return err
	}
	before, after := 0, 0
	for i := range trials {
		before += len(trials[i].Checklist)
		after += len(split[i].Checklist)
	}
	fmt.Printf("split %d trials: %d checklist items → %d facts → %s\n", len(split), before, after, *out)
	return eval.WriteTrials(*out, split)
}

func evalTrialsVerify(args []string) error {
	fs := flag.NewFlagSet("eval trials-verify", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	in := fs.String("in", "data/eval/continuation-v2.jsonl", "trial set to verify")
	out := fs.String("out", "data/eval/continuation-v3.jsonl", "trials whose claims are grounded before the cutoff")
	report := fs.String("report", "data/eval/trial-verify-v3.json", "per-trial verification report")
	workers := fs.Int("workers", 4, "parallel calls")
	fs.Parse(args)
	trials, err := eval.ReadTrials(*in)
	if err != nil {
		return err
	}
	kept, reps, err := eval.VerifyTrials(context.Background(), *corpusRoot, trials, *workers)
	if err != nil {
		return err
	}
	var in0, k, u, tk, bc, add int
	for i, r := range reps {
		in0 += len(trials[i].Checklist)
		k, u, tk, bc, add = k+r.Kept, u+r.Unsupported, tk+r.Task, bc+r.BadCites, add+r.Added
	}
	b, _ := json.MarshalIndent(reps, "", "  ")
	if err := os.WriteFile(*report, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("verified %d trials → kept %d: %d claims in, %d kept, %d unsupported, %d task restatements, %d unresolved citations, %d added → %s\n",
		len(trials), len(kept), in0, k, u, tk, bc, add, *out)
	return eval.WriteTrials(*out, kept)
}
