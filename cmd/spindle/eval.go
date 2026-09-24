package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"spindle/internal/eval"
	"spindle/internal/llm"
)

func runEval(args []string) error {
	if len(args) < 1 {
		return errors.New("eval: want mine | label | run | report | continue")
	}
	switch args[0] {
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
	corpusRoot := fs.String("corpus", "data/corpus", "corpus root")
	episodeRoot := fs.String("episodes", defaultEpisodes, "episode root")
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
	corpusRoot := fs.String("corpus", "data/corpus", "corpus root")
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
	corpusRoot := fs.String("corpus", "data/corpus", "corpus root")
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
