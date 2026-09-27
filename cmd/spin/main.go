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
  dream    fold a project's episodes into project memory (working state, history, superseded)
  snapshot build a leak-free view of spindle data before a time (for trials)
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
	case "dream":
		err = runDream(os.Args[2:])
	case "snapshot":
		err = runSnapshot(os.Args[2:])
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
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin ingest [flags] [transcript.jsonl ...]\n\nWith no files, ingests every Codex and Claude session. With files, ingests only those.")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	var sessions, chunks, skipped int
	ingest := func(read func(string) (corpus.Session, bool, error), paths []string) error {
		for _, p := range paths {
			n, err := ingestTranscript(read, p, *out)
			if err != nil {
				return err
			}
			if n == 0 {
				skipped++
				continue
			}
			chunks += n
			sessions++
		}
		return nil
	}

	if fs.NArg() > 0 {
		for _, p := range fs.Args() {
			abs, err := filepath.Abs(p)
			if err != nil {
				return err
			}
			read := corpus.ReadClaudeSession
			if within(abs, *codexHome) {
				read = corpus.ReadCodexSession
			} else if !within(abs, *claudeProjects) {
				return fmt.Errorf("%s is not under --codex-home or --claude-projects", p)
			}
			if err := ingest(read, []string{abs}); err != nil {
				return err
			}
		}
		fmt.Printf("ingested %d sessions into %d chunks under %s (%d skipped: subagent or empty)\n", sessions, chunks, *out, skipped)
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

// ingestTranscript normalizes one source transcript into corpus chunks and
// returns how many chunks it wrote; 0 means the session was skipped
// (a subagent or empty session).
func ingestTranscript(read func(string) (corpus.Session, bool, error), path, out string) (int, error) {
	s, ok, err := read(path)
	if err != nil {
		return 0, err
	}
	if !ok || len(s.Items) == 0 {
		return 0, nil
	}
	chunks := corpus.SplitChunks(s)
	for _, c := range chunks {
		if _, err := c.Write(out); err != nil {
			return 0, err
		}
	}
	return len(chunks), nil
}

// within reports whether path is inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// cwdMatches reports whether a session belongs to a project slice.
func cwdMatches(cwd, slice string) bool {
	return slice == "" || strings.Contains(strings.ToLower(cwd), strings.ToLower(slice))
}
