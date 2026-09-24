package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"spindle/internal/wake"
)

func retrievalFlags(fs *flag.FlagSet) (corpus, episodes, scope, query *string, limit *int, asJSON *bool) {
	corpus = fs.String("corpus", "data/corpus", "corpus root")
	episodes = fs.String("episodes", defaultEpisodes, "episode root")
	scope = fs.String("scope", "", "optional project, task, or other scope text")
	query = fs.String("query", "", "retrieval query")
	limit = fs.Int("limit", 8, "maximum results")
	asJSON = fs.Bool("json", false, "write machine-readable JSON")
	return
}

func runWake(args []string) error {
	fs := flag.NewFlagSet("wake", flag.ExitOnError)
	corpus, episodes, scope, query, limit, asJSON := retrievalFlags(fs)
	fs.Parse(args)
	return printWakeResults("Wake", wake.Options{CorpusRoot: *corpus, EpisodeRoot: *episodes, Scope: *scope, Query: *query, Limit: *limit}, *asJSON)
}

func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	episodes := fs.String("episodes", defaultEpisodes, "episode root")
	source := fs.String("source", "", "source name, e.g. codex")
	session := fs.String("session", "", "source session ID")
	asJSON := fs.Bool("json", false, "write machine-readable JSON")
	fs.Parse(args)
	if *source == "" || *session == "" {
		return fmt.Errorf("resume requires --source and --session")
	}
	result, err := wake.Resume(wake.Options{EpisodeRoot: *episodes}, *source, *session)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Println("# Resume")
	if result.Latest != nil {
		text, err := wake.Read(wake.Options{EpisodeRoot: *episodes}, result.Latest.Ref)
		if err != nil {
			return err
		}
		fmt.Print("\n## Latest usable episode\n\n")
		fmt.Print(text)
	} else {
		fmt.Println("\nNo usable episode was recorded; start from the source-only range below.")
	}
	if len(result.SourceOnly) > 0 {
		fmt.Println("\n## Source-only ranges to expand")
		for _, source := range result.SourceOnly {
			fmt.Printf("- `%s` — %s\n", source.Ref, source.Title)
		}
	}
	return nil
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	corpus, episodes, scope, query, limit, asJSON := retrievalFlags(fs)
	fs.Parse(args)
	return printResults("Search", wake.Options{CorpusRoot: *corpus, EpisodeRoot: *episodes, Scope: *scope, Query: *query, Limit: *limit}, *asJSON)
}

func printResults(title string, o wake.Options, asJSON bool) error {
	results, err := wake.Search(o)
	return renderResults(title, results, err, asJSON)
}

func printWakeResults(title string, o wake.Options, asJSON bool) error {
	results, err := wake.Wake(o)
	return renderResults(title, results, err, asJSON)
}

func renderResults(title string, results []wake.Result, err error, asJSON bool) error {
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(results)
	}
	fmt.Printf("# %s\n\n", title)
	if len(results) == 0 {
		fmt.Println("No matching recorded context.")
		return nil
	}
	for _, r := range results {
		fmt.Printf("## %s — %s\n%s\n%s\n\n", r.Kind, r.Title, r.Ref, r.Excerpt)
	}
	return nil
}

func runRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	corpus := fs.String("corpus", "data/corpus", "corpus root")
	episodes := fs.String("episodes", defaultEpisodes, "episode root")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("read requires one episode, transcript, or source item reference")
	}
	text, err := wake.Read(wake.Options{CorpusRoot: *corpus, EpisodeRoot: *episodes}, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

func runRelated(args []string) error {
	fs := flag.NewFlagSet("related", flag.ExitOnError)
	episodes := fs.String("episodes", defaultEpisodes, "episode root")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("related requires one episode: reference")
	}
	links, err := wake.Related(wake.Options{EpisodeRoot: *episodes}, fs.Arg(0))
	if err != nil {
		return err
	}
	if len(links) == 0 {
		fmt.Println("No explicit continuation links.")
		return nil
	}
	for _, link := range links {
		fmt.Printf("%s — %s\n", link.Ref, strings.TrimSpace(link.Why))
	}
	return nil
}
