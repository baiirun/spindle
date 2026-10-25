package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"spindle/internal/hooks"
	"spindle/internal/mcp"
	"spindle/internal/memory"
)

const memoryUsage = `usage: spin memory <command> [flags]

commands:
  status    show a project's memory: Now summary age, record size, cursors, dream gate
  now       print the project's Now summary
  search    search the decision record and past user messages
  fold      fold one transcript into its project now (what the worker does after a Stop)
  worker    drain the hook queue and exit (hooks start it; safe to run by hand)
  dream     tidy the decision record now, ignoring the 24 h / N sessions gate with --force
  install   print the hook and MCP config for claude or codex
`

func runMemory(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, memoryUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "status":
		return memoryStatus(args[1:])
	case "now":
		return memoryNow(args[1:])
	case "search":
		return memorySearch(args[1:])
	case "fold":
		return memoryFold(args[1:])
	case "worker":
		return memoryWorker(args[1:])
	case "dream":
		return memoryDream(args[1:])
	case "install":
		return memoryInstall(args[1:])
	}
	fmt.Fprint(os.Stderr, memoryUsage)
	os.Exit(2)
	return nil
}

// runHook handles one hook event from Claude Code or Codex. It always exits
// 0: a memory failure must never block or break the user's session.
func runHook(args []string) error {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	agent := fs.String("agent", "", "claude or codex (default: detect from the transcript)")
	fs.Parse(args)
	h := hooks.Handler{Home: roots.Home, Agent: *agent, Spawn: spawnWorker}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Run(ctx, os.Stdin, os.Stdout); err != nil {
		hooks.Logf(roots.Home, "hook: %v", err)
	}
	return nil
}

// spawnWorker starts a detached worker that outlives the hook process. The
// worker's own model calls carry SPINDLE_INNER, so hooks they trigger are no-ops.
func spawnWorker() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(roots.Home, 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(roots.Home, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "memory", "worker")
	cmd.Env = append(os.Environ(), memory.InnerEnv+"=1")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	dir := fs.String("project", "", "project directory (default: the working directory)")
	fs.Parse(args)
	p, err := memory.ResolveProject(roots.Home, *dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := mcp.Server{Name: "spindle", Version: "0.1.0", Tools: mcp.MemoryTools(p, roots.Corpus)}
	return s.Serve(ctx, os.Stdin, os.Stdout)
}

func projectFlag(fs *flag.FlagSet) *string {
	return fs.String("project", "", "project directory (default: the working directory)")
}

func memoryStatus(args []string) error {
	fs := flag.NewFlagSet("memory status", flag.ExitOnError)
	dir := projectFlag(fs)
	all := fs.Bool("all", false, "list every project")
	fs.Parse(args)
	if *all {
		ps, err := memory.Projects(roots.Home)
		if err != nil {
			return err
		}
		for _, p := range ps {
			rec, _ := p.LoadRecord()
			fmt.Printf("%-40s %4d entries  %s\n", p.Slug, len(rec.Entries), p.Root)
		}
		return nil
	}
	p, err := memory.ResolveProject(roots.Home, *dir)
	if err != nil {
		return err
	}
	rec, err := p.LoadRecord()
	if err != nil {
		return err
	}
	state, err := p.LoadState()
	if err != nil {
		return err
	}
	users, err := p.UserMessages()
	if err != nil {
		return err
	}
	now, _ := p.Now()
	fmt.Printf("project   %s\nmemory    %s\n", p.Root, p.Dir)
	fmt.Printf("record    %d entries, %d changes logged, as of %s\n", len(rec.Entries), len(rec.Log), rec.AsOf.Format(time.RFC3339))
	fmt.Printf("users     %d messages\n", len(users))
	if line, _, _ := strings.Cut(now, "\n"); line != "" {
		fmt.Printf("now       %s\n", strings.Trim(line, "_"))
	} else {
		fmt.Println("now       (none yet)")
	}
	fmt.Printf("dream     last %s, %d session(s) since (due at %d and %s)\n", orNever(state.LastDream), len(state.SessionsSince), memory.DreamSessions, memory.DreamEvery)
	for _, c := range state.Cursors {
		fmt.Printf("cursor    %s %s line %d, %d chars pending, folded %d× (last %s)\n", c.Agent, short8(c.Session), c.Line, c.Pending, c.Extracted, orNever(c.FoldedAt))
	}
	return nil
}

func memoryNow(args []string) error {
	fs := flag.NewFlagSet("memory now", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Parse(args)
	p, err := memory.ResolveProject(roots.Home, *dir)
	if err != nil {
		return err
	}
	now, err := p.Now()
	if err != nil {
		return err
	}
	fmt.Print(now)
	return nil
}

func memorySearch(args []string) error {
	fs := flag.NewFlagSet("memory search", flag.ExitOnError)
	dir := projectFlag(fs)
	limit := fs.Int("limit", 8, "maximum results")
	fs.Parse(args)
	p, err := memory.ResolveProject(roots.Home, *dir)
	if err != nil {
		return err
	}
	tools := mcp.MemoryTools(p, roots.Corpus)
	out, err := tools[0].Call(context.Background(), []byte(fmt.Sprintf(`{"query":%q,"limit":%d}`, strings.Join(fs.Args(), " "), *limit)))
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func memoryFold(args []string) error {
	fs := flag.NewFlagSet("memory fold", flag.ExitOnError)
	dir := projectFlag(fs)
	agent := fs.String("agent", "", "claude or codex (default: detect)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin memory fold [flags] transcript.jsonl\n\nFolds everything past the cursor, below the threshold too. The project defaults to the session's cwd.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	path, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	root := *dir
	if root == "" {
		s, ok, err := memory.ReadTranscript(path, *agent)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s: subagent or empty session", path)
		}
		root = s.Cwd
	}
	p, err := memory.ResolveProject(roots.Home, root)
	if err != nil {
		return err
	}
	res, err := memory.Extract(context.Background(), p, path, *agent, true, memory.ExtractOptions{Model: memory.DefaultModel(), Corpus: roots.Corpus})
	if err != nil {
		return err
	}
	fmt.Printf("%s: folded=%v, %d chars, %d user messages, record +%d ~%d -%d\n", p.Name(), res.Folded, res.NewChars, res.Users, res.Record.Added, res.Record.Replaced, res.Record.Dropped)
	return nil
}

func memoryWorker(args []string) error {
	fs := flag.NewFlagSet("memory worker", flag.ExitOnError)
	noDream := fs.Bool("no-dream", false, "skip the dream gate check")
	sessions := fs.Int("dream-sessions", memory.DreamSessions, "sessions since the last dream before the next one")
	every := fs.Duration("dream-every", memory.DreamEvery, "time since the last dream before the next one")
	fs.Parse(args)
	os.Setenv(memory.InnerEnv, "1") // model calls run Codex; its hooks must not call back in
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return memory.Work(ctx, roots.Home, memory.WorkerOptions{
		Model: memory.DefaultModel(), Corpus: roots.Corpus, Log: os.Stdout,
		DreamEvery: *every, DreamSessions: *sessions, NoDream: *noDream,
	})
}

func memoryDream(args []string) error {
	fs := flag.NewFlagSet("memory dream", flag.ExitOnError)
	dir := projectFlag(fs)
	force := fs.Bool("force", false, "run even if the gate is closed")
	fs.Parse(args)
	p, err := memory.ResolveProject(roots.Home, *dir)
	if err != nil {
		return err
	}
	state, err := p.LoadState()
	if err != nil {
		return err
	}
	if !*force && !memory.DreamDue(state, time.Now(), memory.DreamEvery, memory.DreamSessions) {
		fmt.Printf("not due: %d session(s) since the last dream (%s); use --force\n", len(state.SessionsSince), orNever(state.LastDream))
		return nil
	}
	os.Setenv(memory.InnerEnv, "1")
	res, err := memory.Dream(context.Background(), p, memory.DefaultModel(), time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("dreamed %s: %d replaced, %d dropped, %d skipped\n", p.Name(), res.Replaced, res.Dropped, res.Skipped)
	return nil
}

func memoryInstall(args []string) error {
	fs := flag.NewFlagSet("memory install", flag.ExitOnError)
	bin := fs.String("bin", "", "path to the spin binary (default: this one)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin memory install [--bin PATH] claude|codex\n\nPrints the config to merge by hand; nothing is written.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	spin := *bin
	if spin == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		spin = exe
	}
	out, err := memory.InstallConfig(fs.Arg(0), spin)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func orNever(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
