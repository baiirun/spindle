// Command spindle turns agent transcripts into evaluable memory.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"spindle/internal/corpus"
)

const usage = `usage: spindle <command> [flags]

commands:
  ingest   normalize Codex and Claude transcripts into Markdown chunks
  sleep    extract decision notes from corpus chunks
  eval     mine | label | run | report
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = runIngest(os.Args[2:])
	case "sleep":
		err = runSleep(os.Args[2:])
	case "eval":
		err = runEval(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spindle:", err)
		os.Exit(1)
	}
}

func runIngest(args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	codexHome := fs.String("codex-home", filepath.Join(home, ".codex"), "Codex home directory")
	claudeProjects := fs.String("claude-projects", filepath.Join(home, ".claude", "projects"), "Claude Code projects directory")
	out := fs.String("out", "data/corpus", "corpus output root")
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
