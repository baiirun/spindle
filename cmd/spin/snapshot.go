package main

import (
	"flag"
	"fmt"
	"time"

	"spindle/internal/episode"
	"spindle/internal/snapshot"
)

func runSnapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	corpusRoot := fs.String("corpus", roots.Corpus, "corpus root")
	episodes := fs.String("episodes", roots.Episodes, "episode root")
	before := fs.String("before", "", "cutoff time, RFC 3339 (required); nothing at or after it is included")
	out := fs.String("out", "", "new snapshot directory (required); use it as SPINDLE_HOME")
	fs.Parse(args)
	if *before == "" || *out == "" {
		return fmt.Errorf("snapshot requires --before and --out")
	}
	cutoff, err := time.Parse(time.RFC3339, *before)
	if err != nil {
		return fmt.Errorf("--before: %w", err)
	}
	st, err := snapshot.Build(snapshot.Options{CorpusRoot: *corpusRoot, EpisodeRoot: *episodes, Out: *out, Version: episode.PromptVersion, Before: cutoff})
	if err != nil {
		return err
	}
	fmt.Printf("snapshot before %s: %d chunks (%d truncated), %d episodes → %s\n", cutoff.UTC().Format(time.RFC3339), st.Chunks, st.Truncated, st.Episodes, *out)
	return nil
}
