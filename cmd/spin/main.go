// Command spin is spindle's CLI: long-term memory across all of a user's
// projects for Claude Code, Codex and OpenCode, through their hooks and an
// MCP server. See docs/design.md.
package main

import (
	"context"
	"encoding/json"
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
	"spindle/internal/source"
	"spindle/internal/tui"
)

const usage = `usage: spin <command> [flags]

  index     print what a session here starts with
  log       the memory log across stores (append --gist G adds an entry; -f follows)
  search    search rules, wiki, rollups and the log by words
  zoom      open an id from the index: a week, day, session, entry or page
  rules     list, show, edit or show the history of rule topics
  wiki      show, edit or show the history of a wiki page
  rollup    write the rollups that are due now
  dream     run dream now (--dry-run prints the plan; --due runs every store whose gate is open)
  ingest    read new sessions from a source (claude-code, codex, opencode) or one transcript
  migrate   turn spindle v1 memories into the log, rules and wiki, leaving v1's files as they are
  projects  list every project with its folders and counts
  project   create or describe a project, link or unlink a folder
  sources   where sessions come from and when each was last swept (add KIND, rm KIND)
  activity  what spindle did: starts, tool calls, ingests, rollups, dreams (-f to follow)
  tui       browse the stores in the terminal
  install   print the hook and MCP config for claude, codex or opencode, or the schedule (launchd)
  hook      handle one hook event (JSON on stdin)
  mcp       serve the memory tools over stdio
  worker    sweep the sources, ingest the queue, write rollups, dream what's due (hooks start it)
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
	case "index":
		return runIndex(home, args)
	case "log":
		return runLog(home, args)
	case "search":
		return runSearch(home, args)
	case "zoom":
		return runZoom(home, args)
	case "rules":
		return runPages(home, memory.RulesLayer, args)
	case "wiki":
		return runPages(home, memory.WikiLayer, args)
	case "rollup":
		return runRollup(home, args)
	case "dream":
		return runDream(home, args)
	case "ingest":
		return runIngest(home, args)
	case "migrate":
		return runMigrate(home, args)
	case "projects":
		return runProjects(home, args)
	case "project":
		return runProject(home, args)
	case "sources":
		return runSources(home, args)
	case "activity":
		return runActivity(home, args)
	case "tui":
		return runTUI(home, args)
	case "install":
		return runInstall(home, args)
	case "hook":
		return runHook(home, args)
	case "mcp":
		return runMCP(home, args)
	case "worker":
		return runWorker(home, args)
	}
	fmt.Fprint(os.Stderr, usage)
	os.Exit(2)
	return nil
}

// runHook always exits 0: a memory failure must never block a session.
func runHook(home string, args []string) error {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	agent := fs.String("agent", "", "claude, codex or opencode (default: detect from the transcript)")
	fs.Parse(args)
	h := hooks.Handler{Home: home, Agent: *agent, Spawn: spawn(home)}
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
	agent := fs.String("agent", "", "claude, codex or opencode, for the activity log")
	fs.Parse(args)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	c := memory.NewClient(home, cwd, *name, *agent)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcp.Server{Name: "spindle", Version: "0.3.0", Tools: mcp.MemoryTools(c)}.Serve(ctx, os.Stdin, os.Stdout)
}

// client is the CLI's client for the working directory, or the --project.
func client(home, project string) (memory.Client, error) {
	cwd, err := os.Getwd()
	return memory.NewClient(home, cwd, project, "cli"), err
}

func runIndex(home string, args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	dir := projectFlag(fs)
	fs.Parse(args)
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	text, err := c.Index()
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

// runLog prints log entries across stores, or with "append" adds one:
// spin log [--store S] [--session ID] [-n N] [-f] | spin log append --gist G [--source NOTE] [--global].
func runLog(home string, args []string) error {
	panic("unimplemented")
}

func runSearch(home string, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	dir := projectFlag(fs)
	layers := fs.String("layers", "", "comma-separated: rules, wiki, rollups, log (default: all)")
	n := fs.Int("n", 10, "at most this many results")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: spin search [--layers L] [-n N] WORDS...") }
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	var ls []memory.SearchLayer
	for _, l := range strings.Split(*layers, ",") {
		if l = strings.TrimSpace(l); l != "" {
			ls = append(ls, memory.SearchLayer(l))
		}
	}
	out, err := c.Search(strings.Join(fs.Args(), " "), ls, *n)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func runZoom(home string, args []string) error {
	fs := flag.NewFlagSet("zoom", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin zoom ID   (w:2026-W45, d:2026-11-08, s:SESSION, e-..., rules/SLUG, wiki/SLUG; prefix STORE/ to pick one store)")
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	c, err := client(home, "")
	if err != nil {
		return err
	}
	out, err := c.Zoom(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// runPages is spin rules and spin wiki:
// list | show SLUG | edit SLUG [--reason WHY] (opens $EDITOR) | history SLUG, each with [--store S].
func runPages(home string, layer memory.Layer, args []string) error {
	panic("unimplemented")
}

// runRollup writes every due rollup now, as the worker does after ingest.
func runRollup(home string, args []string) error {
	panic("unimplemented")
}

// runDream is spin dream [--project P] [--dry-run] [--force] | spin dream --due.
// --dry-run prints the plan, rejected edits included, and writes nothing.
func runDream(home string, args []string) error {
	panic("unimplemented")
}

// runIngest is spin ingest SOURCE [--since TIME] (queues every session the
// source changed since then, then runs the worker) | spin ingest FILE.jsonl.
func runIngest(home string, args []string) error {
	panic("unimplemented")
}

func runMigrate(home string, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	fs.Parse(args)
	reports, err := memory.MigrateV1(home, time.Now())
	for _, r := range reports {
		line := fmt.Sprintf("%-16s %4d entries %3d rules %3d pages", r.Store, r.Entries, r.Rules, r.Pages)
		if r.Skip != "" {
			line = fmt.Sprintf("%-16s skipped: %s", r.Store, r.Skip)
		}
		fmt.Println(line)
		for _, e := range r.Errors {
			fmt.Println("  error:", e)
		}
	}
	return err
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

func runSources(home string, args []string) error {
	fs := flag.NewFlagSet("sources", flag.ExitOnError)
	dir := projectFlag(fs)
	from := fs.String("dir", "", "add: the folder the source reads, if not the tool's default")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin sources | spin sources add KIND [--dir DIR] | spin sources rm KIND\nkinds: "+strings.Join(source.Kinds, ", "))
	}
	fs.Parse(args)
	action, kind := "", ""
	if fs.NArg() > 0 {
		action = fs.Arg(0)
		if fs.NArg() < 2 {
			fs.Usage()
			os.Exit(2)
		}
		kind = fs.Arg(1)
		fs.Parse(fs.Args()[2:]) // flags after the kind
	}
	c, err := client(home, *dir)
	if err != nil {
		return err
	}
	var out string
	switch action {
	case "":
		out, err = c.Sources()
	case "add":
		out, err = c.AddSource(kind, *from)
	case "rm", "remove":
		out, err = c.RemoveSource(kind)
	default:
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func runInstall(home string, args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	bin := fs.String("bin", "", "path to spin (default: this binary)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin install [--bin PATH] claude|codex|opencode|launchd\n\nPrints the config to merge by hand; nothing is written.")
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

func runWorker(home string, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	noDream := fs.Bool("no-dream", false, "skip the dream gate check")
	fs.Parse(args)
	_ = os.Setenv(memory.InnerEnv, "1")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return memory.Work(ctx, home, memory.WorkerOptions{Log: os.Stdout, NoDream: *noDream})
}

func runActivity(home string, args []string) error {
	fs := flag.NewFlagSet("activity", flag.ExitOnError)
	project := fs.String("project", "", "only sessions in this project")
	follow := fs.Bool("f", false, "keep printing new events")
	n := fs.Int("n", 50, "show the last n events (0: all)")
	session := fs.String("session", "", "only this session (a prefix is enough)")
	kind := fs.String("kind", "", "only this kind: start, call, ingest, rollup, dream, sweep, migrate")
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

func runTUI(home string, args []string) error {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	start := fs.String("project", "", "the project to open on (default: this folder's, else global)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin tui [--project P]\n\nA read-only viewer: ←/→ or tab move between panes, ↑/↓ or j/k move, b/h/a show the body, history or activity, / searches, r reloads, q quits.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	c, err := client(home, "")
	if err != nil {
		return err
	}
	context := c.Context().Name
	open := *start
	if open == "" {
		open = context
	}
	if open == "" {
		open = memory.GlobalName
	}
	return tui.Run(home, memory.NameOf(open), context)
}
