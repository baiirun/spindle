// Command spin is the spindle CLI: it turns agent transcripts into durable,
// searchable context for continuing work.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"spindle/internal/corpus"
)

const usage = `usage: spin <command> [flags]

commands:
  ingest   normalize Codex and Claude transcripts into Markdown chunks
  sleep    project one source session into durable handoff episodes
  wake     return a bounded, read-only continuity context
  resume   return the startup packet for a known prior session
  search   find episodes and transcript passages
  read     expand an episode or transcript reference
  related  follow explicit episode continuation links
  eval     mine | label | run | report | continue
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := loadRoots(); err != nil {
		fmt.Fprintln(os.Stderr, "spin:", err)
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = runIngest(os.Args[2:])
	case "sleep":
		err = runSleep(os.Args[2:])
	case "wake":
		err = runWake(os.Args[2:])
	case "resume":
		err = runResume(os.Args[2:])
	case "search":
		err = runSearch(os.Args[2:])
	case "read":
		err = runRead(os.Args[2:])
	case "related":
		err = runRelated(os.Args[2:])
	case "eval":
		err = runEval(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spin:", err)
		os.Exit(1)
	}
}

func runIngest(args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	codexHome := fs.String("codex-home", filepath.Join(home, ".codex"), "Codex home directory")
	claudeProjects := fs.String("claude-projects", filepath.Join(home, ".claude", "projects"), "Claude Code projects directory")
	out := fs.String("out", roots.Corpus, "corpus output root")
	fs.Parse(args)

	var sessions, chunks, skipped int
	ingest := func(read func(string) (corpus.Session, bool, error), paths []string) error {
		for _, p := range paths {
			s, ok, err := read(p)
			if err != nil {
				return err
			}
			if !ok || len(s.Items) == 0 {
				skipped++
				continue
			}
			for _, c := range corpus.SplitChunks(s) {
				if _, err := c.Write(*out); err != nil {
					return err
				}
				chunks++
			}
			sessions++
		}
		return nil
	}

	codexPaths, err := corpus.CodexSessionPaths(*codexHome)
	if err != nil {
		return err
	}
	if err := ingest(corpus.ReadCodexSession, codexPaths); err != nil {
		return err
	}
	claudePaths, err := corpus.ClaudeSessionPaths(*claudeProjects)
	if err != nil {
		return err
	}
	if err := ingest(corpus.ReadClaudeSession, claudePaths); err != nil {
		return err
	}
	fmt.Printf("ingested %d sessions into %d chunks under %s (%d skipped: subagent or empty)\n", sessions, chunks, *out, skipped)
	return nil
}

// cwdMatches reports whether a session belongs to a project slice.
func cwdMatches(cwd, slice string) bool {
	return slice == "" || strings.Contains(strings.ToLower(cwd), strings.ToLower(slice))
}
