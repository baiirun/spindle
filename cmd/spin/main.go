// Command spin is spindle's CLI: long-term project memory for Claude Code and
// Codex, through their hooks and an MCP server. See docs/design.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
  status    how many memories, last dream, how far each session was read
  show      print one memory in full
  search    find memories by words in their name, description or body
  write     create or replace a memory (body on stdin)
  rm        delete a memory
  index     print MEMORY.md
  log       show the project's memory activity: recalls, saves, tool calls (-f to follow)
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
	case "show":
		return runShow(home, args)
	case "search":
		return runSearch(home, args)
	case "write":
		return runWrite(home, args)
	case "rm":
		return runRm(home, args)
	case "index":
		return runIndex(home, args)
	case "log":
		return runLog(home, args)
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
	agent := fs.String("agent", "", "claude or codex, for the activity log")
	fs.Parse(args)
	p, err := memory.Resolve(home, *dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcp.Server{Name: "spindle", Version: "0.1.0", Tools: mcp.MemoryTools(p, *agent)}.Serve(ctx, os.Stdin, os.Stdout)
}

// client resolves the project from --project or the working directory.
func client(home, dir string) (memory.Client, error) {
	p, err := memory.Resolve(home, dir)
	return p.Client("cli"), err
}

func runStatus(home string, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Parse(args)
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	s, err := c.Status()
	if err != nil {
		return err
	}
	fmt.Print(memory.FormatStatus(s))
	return nil
}

func runShow(home string, args []string) error {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin show [--project DIR] NAME") }
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	m, err := c.Read(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(memory.FormatMemory(m))
	return nil
}

func runSearch(home string, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	dir := projectFlag(fs)
	n := fs.Int("n", 10, "at most this many results")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin search [--project DIR] [-n N] WORDS...") }
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	hits, total, err := c.Search(strings.Join(fs.Args(), " "), *n)
	if err != nil {
		return err
	}
	fmt.Print(memory.FormatSearch(hits, total))
	return nil
}

func runWrite(home string, args []string) error {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	dir := projectFlag(fs)
	typ := fs.String("type", "", "user, directive, project or reference (default: keep the existing type)")
	desc := fs.String("description", "", "one line, used to decide relevance (default: keep the existing one)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin write [--project DIR] [--type T] [--description D] NAME < body.md\n\nThe body is read from stdin and replaces the whole memory.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	msg, err := c.Write(memory.Op{Name: fs.Arg(0), Type: *typ, Description: *desc, Body: string(body)})
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func runRm(home string, args []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	dir := projectFlag(fs)
	reason := fs.String("reason", "", "why, for the change log")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin rm [--project DIR] [--reason WHY] NAME") }
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	msg, err := c.Delete(fs.Arg(0), *reason)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func runIndex(home string, args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Parse(args)
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	fmt.Print(c.Index())
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

func runLog(home string, args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	dir := projectFlag(fs)
	follow := fs.Bool("f", false, "keep printing new events")
	n := fs.Int("n", 50, "show the last n events (0: all)")
	session := fs.String("session", "", "only this session (a prefix is enough)")
	kind := fs.String("kind", "", "only this kind: start, select, recall, skip, call, extract, dream")
	asJSON := fs.Bool("json", false, "print raw JSON lines")
	fs.Parse(args)
	p, err := memory.Resolve(home, *dir)
	if err != nil {
		return err
	}
	keep := func(e memory.Event) bool {
		return strings.HasPrefix(e.Session, *session) && (*kind == "" || e.Kind == *kind)
	}
	print := func(e memory.Event) {
		if *asJSON {
			b, _ := json.Marshal(e)
			fmt.Println(string(b))
		} else {
			fmt.Println(e)
		}
	}
	evs, err := p.Events()
	if err != nil {
		return err
	}
	var shown []memory.Event
	for _, e := range evs {
		if keep(e) {
			shown = append(shown, e)
		}
	}
	if *n > 0 && len(shown) > *n {
		shown = shown[len(shown)-*n:]
	}
	if len(shown) == 0 && !*follow {
		fmt.Printf("no activity yet for %s\n", p.Root)
	}
	for _, e := range shown {
		print(e)
	}
	for seen := len(evs); *follow; time.Sleep(500 * time.Millisecond) {
		if evs, err = p.Events(); err != nil {
			return err
		}
		for _, e := range evs[min(seen, len(evs)):] {
			if keep(e) {
				print(e)
			}
		}
		seen = len(evs)
	}
	return nil
}
