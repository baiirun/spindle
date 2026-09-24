package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"spindle/internal/episode"
	"spindle/internal/eval"
	"spindle/internal/llm"
	"spindle/internal/sleep"
)

const defaultNotes = "data/memory/" + sleep.PromptVersion // legacy decision-eval fixture

type linksFlag []episode.Link

func (f *linksFlag) String() string { return fmt.Sprint([]episode.Link(*f)) }

func (f *linksFlag) Set(value string) error {
	ref, why, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(ref) == "" || strings.TrimSpace(why) == "" {
		return fmt.Errorf("continue must be REF=WHY")
	}
	*f = append(*f, episode.Link{Ref: strings.TrimSpace(ref), Why: strings.TrimSpace(why)})
	return nil
}

func runSleep(args []string) error {
	fs := flag.NewFlagSet("sleep", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	out := fs.String("out", roots.Episodes, "episode root")
	source := fs.String("source", "", "source name, e.g. codex")
	session := fs.String("session", "", "source session ID")
	var continues linksFlag
	fs.Var(&continues, "continue", "prior context link as REF=WHY (repeatable)")
	fs.Parse(args)
	if *source == "" || *session == "" {
		return fmt.Errorf("sleep requires --source and --session; an external scheduler chooses when to project")
	}
	episodes, err := episode.Project(context.Background(), episode.Options{
		CorpusRoot: *corpusRoot, OutRoot: *out, Source: *source, Session: *session,
		Model: llm.Extractor, Continues: continues,
	})
	if err != nil {
		return err
	}
	fmt.Printf("sleep %s: wrote %d episode(s) → %s\n", episode.PromptVersion, len(episodes), *out)
	return nil
}

func evalRun(args []string) error {
	fs := flag.NewFlagSet("eval run", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	itemsPath := fs.String("items", "data/eval/decisions-v0.jsonl", "labeled eval set")
	armName := fs.String("arm", "D1", "D0 | D1 | D2 | D2n")
	notes := fs.String("notes", defaultNotes, "decision notes root for D2/D2n")
	minConf := fs.String("confidence", "high", "minimum label confidence: high | medium | low")
	limit := fs.Int("limit", 0, "only the first N items (0 = all)")
	workers := fs.Int("workers", 3, "parallel items")
	maxTurns := fs.Int("max-turns", 15, "reader turn limit")
	tag := fs.String("tag", "", "label for this run")
	fs.Parse(args)

	arm, ok := eval.Arms(*notes)[*armName]
	if !ok {
		return fmt.Errorf("unknown arm %q", *armName)
	}
	all, err := eval.ReadItems(*itemsPath)
	if err != nil {
		return err
	}
	rank := map[string]int{"high": 3, "medium": 2, "low": 1}
	var items []eval.Item
	for _, it := range all {
		if rank[it.Confidence] >= rank[*minConf] {
			items = append(items, it)
		}
	}
	if *limit > 0 && len(items) > *limit {
		items = items[:*limit]
	}
	name := time.Now().Format("20060102-150405") + "-" + arm.Name
	if *tag != "" {
		name += "-" + *tag
	}
	runDir := filepath.Join("runs", name)
	results, runErr := eval.Run(context.Background(), eval.RunOptions{
		CorpusRoot: *corpusRoot, Arm: arm, Items: items, RunDir: runDir,
		ScratchDir: filepath.Join("/private/tmp/spindle-eval", name), Workers: *workers, MaxTurns: *maxTurns,
	})
	s := eval.Summarize(arm.Name, results)
	if err := eval.WriteSummary(runDir, s); err != nil {
		return err
	}
	printSummaries([]namedSummary{{name, s}})
	return runErr
}

type namedSummary struct {
	name string
	s    eval.Summary
}

func evalReport(args []string) error {
	fs := flag.NewFlagSet("eval report", flag.ExitOnError)
	fs.Parse(args)
	dirs := fs.Args()
	if len(dirs) == 0 {
		dirs, _ = filepath.Glob("runs/*")
	}
	sort.Strings(dirs)
	var rows []namedSummary
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "summary.json"))
		if err != nil {
			continue
		}
		var s eval.Summary
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		rows = append(rows, namedSummary{filepath.Base(d), s})
	}
	printSummaries(rows)
	return nil
}

func printSummaries(rows []namedSummary) {
	cols := []string{"n", "correct", "contradiction", "abstained", "attribution", "citations_valid", "reached", "turns", "input_tokens", "output_tokens", "reasoning_tokens"}
	fmt.Printf("%-36s %s\n", "run", strings.Join(cols, "  "))
	for _, r := range rows {
		var vals []string
		for _, c := range cols {
			v, ok := r.s.Metrics[c]
			if !ok {
				vals = append(vals, fmt.Sprintf("%*s", len(c), "-"))
				continue
			}
			vals = append(vals, fmt.Sprintf("%*.2f", len(c), v))
		}
		fmt.Printf("%-36s %s   errors=%d\n", r.name, strings.Join(vals, "  "), r.s.Errors)
		kinds := make([]string, 0, len(r.s.ByKind))
		for k := range r.s.ByKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			m := r.s.ByKind[k]
			fmt.Printf("  %-9s n=%-3.0f correct=%.2f contradiction=%.2f abstained=%.2f\n", k, m["n"], m["correct"], m["contradiction"], m["abstained"])
		}
	}
}
