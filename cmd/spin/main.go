// Command spin is spindle's CLI: long-term project memory for Claude Code and
// Codex, through their hooks and an MCP server. See docs/design.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"spindle/internal/hooks"
	"spindle/internal/mcp"
	"spindle/internal/memory"
	"spindle/internal/model"
)

const usage = `usage: spin <command> [flags]

  hook      handle one Claude Code or Codex hook event (JSON on stdin)
  mcp       serve the project's memory tools over stdio
  status    show a project's memory
  install   print the hook and MCP config for claude or codex
  dream     run the consolidation pass now (--force ignores the 24 h / 5 sessions gate)
  extract   run extraction on one transcript now
  worker    drain the extraction queue (hooks start it)
  select    run the recall selector for a pending message (hooks start it)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	home, err := memory.Home()
	if err == nil {
		err = run(home, os.Args[1], os.Args[2:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spin:", err)
		os.Exit(1)
	}
}

func run(home, cmd string, args []string) error {
	switch cmd {
	case "hook":
		return runHook(home, args)
	case "mcp":
		return runMCP(home, args)
	case "status":
		return runStatus(home, args)
	case "install":
		return runInstall(args)
	case "dream":
		return runDream(home, args)
	case "extract":
		return runExtract(home, args)
	case "worker":
		return runWorker(home, args)
	case "select":
		return runSelect(home, args)
	}
	fmt.Fprint(os.Stderr, usage)
	os.Exit(2)
	return nil
}

// runHook always exits 0: a memory failure must never block a session.
func runHook(home string, args []string) error {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	agent := fs.String("agent", "", "claude or codex (default: detect from the transcript)")
	fs.Parse(args)
	wait := 4 * time.Second
	if v, err := strconv.ParseFloat(os.Getenv("SPINDLE_RECALL_WAIT"), 64); err == nil {
		wait = time.Duration(v * float64(time.Second))
	}
	h := hooks.Handler{Home: home, Agent: *agent, Spawn: spawn(home), RecallWait: wait, LocalRecall: os.Getenv("SPINDLE_RECALL") == "local"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.Run(ctx, os.Stdin, os.Stdout); err != nil {
		hooks.Logf(home, "hook: %v", err)
	}
	return nil
}

// spawn starts a detached spin that outlives the hook. Its environment
// carries SPINDLE_INNER, so hooks fired by its own claude or codex calls do nothing.
func spawn(home string) func(args ...string) error {
	return func(args ...string) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(home, 0o755); err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(home, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer log.Close()
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), memory.InnerEnv+"=1")
		cmd.Stdout, cmd.Stderr = log, log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
}

func projectFlag(fs *flag.FlagSet) *string {
	return fs.String("project", "", "project directory (default: the working directory)")
}

func runMCP(home string, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Parse(args)
	p, err := memory.Resolve(home, *dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcp.Server{Name: "spindle", Version: "0.1.0", Tools: mcp.MemoryTools(p)}.Serve(ctx, os.Stdin, os.Stdout)
}

func runStatus(home string, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Parse(args)
	p, err := memory.Resolve(home, *dir)
	if err != nil {
		return err
	}
	mems, err := p.Load()
	if err != nil {
		return err
	}
	st, err := p.LoadState()
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, m := range mems {
		counts[m.Type]++
	}
	fmt.Printf("project  %s\nmemory   %s\n", p.Root, p.MemDir())
	fmt.Printf("files    %d (user %d, directive %d, project %d, reference %d)\n", len(mems), counts["user"], counts["directive"], counts["project"], counts["reference"])
	last := "never"
	if !st.LastDream.IsZero() {
		last = st.LastDream.Local().Format("2006-01-02 15:04")
	}
	fmt.Printf("dream    last %s; %d of %d sessions since, due after %s\n", last, len(st.Sessions), memory.DreamSessions, memory.DreamEvery)
	for path, c := range st.Cursors {
		fmt.Printf("cursor   %s line %d, %s (%s)\n", c.Agent, c.Line, c.At.Local().Format("2006-01-02 15:04"), path)
	}
	return nil
}

func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	bin := fs.String("bin", "", "path to spin (default: this binary)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin install [--bin PATH] claude|codex\n\nPrints the config to merge by hand; nothing is written.")
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	path := *bin
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		path = exe
	}
	out, err := hooks.InstallConfig(fs.Arg(0), path)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func runDream(home string, args []string) error {
	fs := flag.NewFlagSet("dream", flag.ExitOnError)
	dir := projectFlag(fs)
	force := fs.Bool("force", false, "run even if the gate is closed")
	agent := fs.String("agent", "claude", "which CLI runs the model: claude or codex")
	fs.Parse(args)
	p, err := memory.Resolve(home, *dir)
	if err != nil {
		return err
	}
	st, err := p.LoadState()
	if err != nil {
		return err
	}
	if !*force && !memory.DreamDue(st, time.Now()) {
		fmt.Printf("not due: %d of %d sessions since the last dream; --force to run anyway\n", len(st.Sessions), memory.DreamSessions)
		return nil
	}
	os.Setenv(memory.InnerEnv, "1")
	res, err := memory.Dream(context.Background(), p, model.For(*agent, model.Dream), time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("%d written, %d deleted, %d skipped\n%s\n", res.Written, res.Deleted, res.Skipped, res.Summary)
	return nil
}

func runExtract(home string, args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin extract [--project DIR] transcript.jsonl") }
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	path, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	agent, err := memory.DetectAgent(path)
	if err != nil {
		return err
	}
	root := *dir
	if root == "" {
		if root, err = memory.TranscriptCwd(path); err != nil {
			return err
		}
	}
	p, err := memory.Resolve(home, root)
	if err != nil {
		return err
	}
	os.Setenv(memory.InnerEnv, "1")
	res, err := memory.Extract(context.Background(), p, path, model.For(agent, model.Extract), time.Now())
	if err != nil {
		return err
	}
	if res.Skip != "" {
		fmt.Println("skipped:", res.Skip)
		return nil
	}
	fmt.Printf("%s: %d written, %d deleted, %d skipped\n", p.Name(), res.Written, res.Deleted, res.Result.Skipped)
	return nil
}

func runWorker(home string, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	noDream := fs.Bool("no-dream", false, "skip the dream gate check")
	fs.Parse(args)
	os.Setenv(memory.InnerEnv, "1")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return memory.Work(ctx, home, memory.WorkerOptions{Log: os.Stdout, NoDream: *noDream})
}

func runSelect(home string, args []string) error {
	fs := flag.NewFlagSet("select", flag.ExitOnError)
	root := fs.String("root", "", "project root")
	session := fs.String("session", "", "session ID")
	id := fs.String("id", "", "pending selection ID")
	agent := fs.String("agent", "claude", "claude or codex")
	fs.Parse(args)
	if *root == "" || *id == "" {
		return fmt.Errorf("--root and --id are required")
	}
	os.Setenv(memory.InnerEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return memory.At(home, *root).RunSelect(ctx, *session, *id, model.For(*agent, model.Select), false)
}
