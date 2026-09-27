package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindle/internal/dream"
	"spindle/internal/llm"
)

func runDream(args []string) error {
	fs := flag.NewFlagSet("dream", flag.ExitOnError)
	project := fs.String("project", "", "project name (required)")
	sessions := fs.String("sessions", "", "comma-separated source:session handles that belong to the project (required)")
	batch := fs.Int("batch", 8, "episodes folded per step")
	cuts := fs.String("cuts", "", "comma-separated RFC3339 times that get their own snapshot")
	until := fs.String("until", "", "fold only episodes that ended before this RFC3339 time")
	out := fs.String("out", "", "output directory (default $SPINDLE_HOME/projects/<project>/<prompt version>)")
	format := fs.String("format", "v2", "memory format: v1 | v2")
	fs.Parse(args)
	if *project == "" || *sessions == "" {
		return fmt.Errorf("dream: --project and --sessions are required")
	}
	f, ok := dream.Formats[*format]
	if !ok {
		return fmt.Errorf("dream: unknown --format %q", *format)
	}
	o := dream.Options{Project: *project, EpisodeRoot: roots.Episodes, Sessions: strings.Split(*sessions, ","),
		Batch: *batch, Out: *out, Model: llm.Reader, Format: f}
	if o.Out == "" {
		o.Out = filepath.Join(roots.Home, "projects", *project, f.Version)
	}
	if *until != "" {
		t, err := time.Parse(time.RFC3339, *until)
		if err != nil {
			return fmt.Errorf("dream: --until: %w", err)
		}
		o.Until = t
	}
	for _, c := range strings.Split(*cuts, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, c)
		if err != nil {
			return fmt.Errorf("dream: --cuts: %w", err)
		}
		o.Cuts = append(o.Cuts, t)
	}
	steps, err := dream.Fold(context.Background(), o)
	b, _ := json.MarshalIndent(steps, "", "  ")
	if werr := os.WriteFile(filepath.Join(o.Out, "steps.json"), b, 0o644); werr != nil && err == nil {
		err = werr
	}
	for _, s := range steps {
		fmt.Printf("step %3d through %s as of %s: %d chars, %d unresolved citations\n", s.N, s.Through, s.AsOf.UTC().Format("2006-01-02 15:04"), s.Chars, s.BadCites)
	}
	if err == nil {
		fmt.Printf("memory → %s\n", filepath.Join(o.Out, "memory.md"))
	}
	return err
}
