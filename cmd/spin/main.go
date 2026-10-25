// Command spin is spindle's CLI: long-term memory across all of a user's
// projects for Claude Code and Codex, through their hooks and an MCP server.
// See docs/design.md.
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
  mcp       serve the memory tools over stdio
  status    this folder's project: how many memories, last dream; counts for the rest
  projects  list every project with its folders and memory count
  project   create or describe a project, link or unlink a folder
  show      print one memory in full
  search    find memories by words in their name, description or body
  write     create or replace a memory (body on stdin)
  rm        delete a memory
  index     print what a session here starts with: this project's index, global, the rest
  history   one memory's changes, who made them and the text before (--restore N puts one back)
  log       show memory activity across projects: recalls, saves, tool calls (-f to follow)
  install   print the hook and MCP config for claude or codex, or the dream schedule (launchd)
  dream     run the consolidation pass now (--force ignores the 24 h / 5 sessions gate; --due runs every due project)
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
	if err != nil && os.Args[1] == "hook" {
		// A hook never fails the session, even with no usable home to log to.
		fmt.Fprintln(os.Stderr, "spin hook:", err)
		return
	}
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
	case "projects":
		return runProjects(home, args)
	case "project":
		return runProject(home, args)
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
	case "history":
		return runHistory(home, args)
	case "log":
		return runLog(home, args)
	case "install":
		return runInstall(home, args)
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
	return fs.String("project", "", "the session's project, or global (default: the one this folder is linked to)")
}

func runMCP(home string, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	name := projectFlag(fs)
	agent := fs.String("agent", "", "claude or codex, for the activity log")
	fs.Parse(args)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	c := memory.NewClient(home, cwd, *name, *agent)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcp.Server{Name: "spindle", Version: "0.2.0", Tools: mcp.MemoryTools(c, func() error {
		p := c.Context()
		if p.Name == "" {
			return fmt.Errorf("this folder isn't linked to a project; link one with memory_project first")
		}
		return spawn(home)("dream", "--project", p.Name, "--force")
	})}.Serve(ctx, os.Stdin, os.Stdout)
}

// client is the CLI's client for the working directory, or the --project.
func client(home, project string) (memory.Client, error) {
	cwd, err := os.Getwd()
	return memory.NewClient(home, cwd, project, "cli"), err
}

func runProjects(home string, args []string) error {
	fs := flag.NewFlagSet("projects", flag.ExitOnError)
	fs.Parse(args)
	c, err := client(home, "")
	if err != nil {
		return err
	}
	ps, err := c.Projects()
	if err != nil {
		return err
	}
	fmt.Print(memory.FormatProjects(ps))
	return nil
}

func runProject(home string, args []string) error {
	fs := flag.NewFlagSet("project", flag.ExitOnError)
	desc := fs.String("description", "", "one line on what the project is about")
	link := fs.String("link", "", "link this folder to the project (. is the working directory's repository root)")
	unlink := fs.String("unlink", "", "unlink this folder")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin project [--description D] [--link DIR] [--unlink DIR] NAME\n\nCreates the project if it doesn't exist. Folders only hint at a project; every project's memories stay visible everywhere.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() > 1 { // flags after the name
		name := fs.Arg(0)
		fs.Parse(fs.Args()[1:])
		fs.Parse(append([]string{name}, fs.Args()...))
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	c, err := client(home, "")
	if err != nil {
		return err
	}
	if *link != "" && *link != "." {
		if *link, err = filepath.Abs(*link); err != nil {
			return err
		}
	}
	if *unlink != "" {
		if *unlink, err = filepath.Abs(*unlink); err != nil {
			return err
		}
	}
	msg, err := c.SetProject(fs.Arg(0), *desc, *link, *unlink)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
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
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin show [--project P] NAME") }
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
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin search [--project P] [-n N] WORDS...") }
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
	global := fs.Bool("global", false, "save to the global store, loaded in every project (or name it global/NAME)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin write [--project P] [--global] [--type T] [--description D] NAME < body.md\n\nThe body is read from stdin and replaces the whole memory. NAME may be PROJECT/NAME.")
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
	o := memory.Op{Name: fs.Arg(0), Type: *typ, Description: *desc, Body: string(body)}
	if *global {
		o.Project = memory.GlobalName
	}
	msg, err := c.Write(o)
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
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin rm [--project P] [--reason WHY] NAME") }
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

func runInstall(home string, args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	bin := fs.String("bin", "", "path to spin (default: this binary)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin install [--bin PATH] claude|codex|launchd\n\nPrints the config to merge by hand; nothing is written.")
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
	if fs.Arg(0) == "launchd" {
		fmt.Print(hooks.LaunchdConfig(path, home))
		return nil
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
	due := fs.Bool("due", false, "dream every project whose gate is open (what the schedule runs)")
	fs.String("agent", "", "ignored: dreams use Codex, then Claude if Codex fails (SPINDLE_BACKEND pins one)")
	fs.Parse(args)
	_ = os.Setenv(memory.InnerEnv, "1")
	m := model.For("", model.Dream)
	if *due {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if n := memory.DreamDueStores(ctx, home, m, time.Now(), os.Stdout); n == 0 {
			fmt.Println("nothing due")
		}
		return nil
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	res, err := c.Dream(context.Background(), m, *force)
	if err != nil {
		return err
	}
	fmt.Printf("%d written, %d deleted, %d skipped\n%s\n", res.Written, res.Deleted, res.Skipped, res.Summary)
	return nil
}

func runExtract(home string, args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin extract [--project P] transcript.jsonl") }
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
	cwd, err := memory.TranscriptCwd(path)
	if err != nil {
		return err
	}
	c := memory.NewClient(home, cwd, *dir, "cli")
	_ = os.Setenv(memory.InnerEnv, "1")
	res, err := c.Extract(context.Background(), path, model.For(agent, model.Extract))
	if err != nil {
		return err
	}
	if res.Skip != "" {
		fmt.Println("skipped:", res.Skip)
		return nil
	}
	fmt.Printf("%d written, %d deleted, %d skipped: %s\n", res.Written, res.Deleted, res.Result.Skipped, strings.Join(res.Changed, " "))
	return nil
}

func runWorker(home string, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	noDream := fs.Bool("no-dream", false, "skip the dream gate check")
	fs.Parse(args)
	_ = os.Setenv(memory.InnerEnv, "1")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return memory.Work(ctx, home, memory.WorkerOptions{Log: os.Stdout, NoDream: *noDream})
}

func runSelect(home string, args []string) error {
	fs := flag.NewFlagSet("select", flag.ExitOnError)
	session := fs.String("session", "", "session ID")
	id := fs.String("id", "", "pending selection ID")
	agent := fs.String("agent", "claude", "claude or codex")
	fs.Parse(args)
	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	_ = os.Setenv(memory.InnerEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return memory.RunSelect(ctx, home, *session, *id, model.For(*agent, model.Select), false)
}

func runLog(home string, args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	project := fs.String("project", "", "only sessions in this project")
	follow := fs.Bool("f", false, "keep printing new events")
	n := fs.Int("n", 50, "show the last n events (0: all)")
	session := fs.String("session", "", "only this session (a prefix is enough)")
	kind := fs.String("kind", "", "only this kind: start, select, recall, skip, call, extract, dream")
	asJSON := fs.Bool("json", false, "print raw JSON lines")
	fs.Parse(args)
	c, err := client(home, "")
	if err != nil {
		return err
	}
	f := memory.LogFilter{Session: *session, Kind: *kind, Project: *project, Last: *n}
	print := func(evs []memory.Event) {
		if !*asJSON {
			fmt.Print(memory.FormatEvents(evs))
			return
		}
		for _, e := range evs {
			b, _ := json.Marshal(e)
			fmt.Println(string(b))
		}
	}
	evs, err := c.Activity(f)
	if err != nil {
		return err
	}
	if len(evs) > 0 || !*follow {
		print(evs)
	}
	all, err := c.Activity(memory.LogFilter{})
	for seen := len(all); *follow && err == nil; time.Sleep(500 * time.Millisecond) {
		if all, err = c.Activity(memory.LogFilter{}); err != nil {
			break
		}
		var fresh []memory.Event
		for _, e := range all[min(seen, len(all)):] {
			if f.Match(e) {
				fresh = append(fresh, e)
			}
		}
		if len(fresh) > 0 {
			print(fresh)
		}
		seen = len(all)
	}
	return err
}

func runHistory(home string, args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	dir := projectFlag(fs)
	full := fs.Bool("full", false, "show each earlier version's text")
	restore := fs.Int("restore", 0, "put back the version change N replaced or deleted")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin history [--project P] [--full] [--restore N] NAME")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	if *restore > 0 {
		msg, err := c.Restore(fs.Arg(0), *restore)
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	}
	hist, err := c.History(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(memory.FormatHistory(fs.Arg(0), hist, *full))
	return nil
}
