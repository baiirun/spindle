// Command spin is spindle's CLI: long-term memory across all of a user's
// projects for Claude Code, Codex and OpenCode, through their hooks and an
// MCP server. See docs/design.md.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	"spindle/internal/model"
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
  dream     run a dream session now (--dry-run records its edits without writing; --due runs every store whose gate is open)
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

// runLog prints log entries across stores, or with "append" adds one.
func runLog(home string, args []string) error {
	if len(args) > 0 && args[0] == "append" {
		fs := flag.NewFlagSet("log append", flag.ExitOnError)
		dir := projectFlag(fs)
		gist := fs.String("gist", "", "one line, at most 280 bytes")
		from := fs.String("source", "", "where it came from: a doc, a URL, a meeting")
		global := fs.Bool("global", false, "about you, or true in every project")
		fs.Parse(args[1:])
		if *gist == "" {
			fmt.Fprintln(os.Stderr, "usage: spin log append --gist G [--source NOTE] [--global] [--project P]")
			os.Exit(2)
		}
		c, err := client(home, *dir)
		if err != nil {
			return err
		}
		scope := memory.ScopeProject
		if *global {
			scope = memory.ScopeGlobal
		}
		msg, err := c.Append(*gist, scope, *from)
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	}
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	store := fs.String("store", "", "only this store (global, unfiled or a project)")
	session := fs.String("session", "", "only this session (a prefix is enough)")
	n := fs.Int("n", 50, "show the last n entries (0: all)")
	follow := fs.Bool("f", false, "keep printing new entries")
	asJSON := fs.Bool("json", false, "print raw JSON lines")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin log [--store S] [--session ID] [-n N] [-f] [--json] | spin log append --gist G [--source NOTE] [--global]")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	c, err := client(home, "")
	if err != nil {
		return err
	}
	f := memory.EntryFilter{Store: *store, Session: *session, Last: *n}
	print := func(es []memory.StoreEntry) {
		for _, e := range es {
			if *asJSON {
				b, _ := json.Marshal(e)
				fmt.Println(string(b))
			} else {
				fmt.Println(e.String())
			}
		}
	}
	es, err := c.Entries(f)
	if err != nil {
		return err
	}
	print(es)
	seen := map[memory.EntryID]bool{}
	for _, e := range es {
		seen[e.ID] = true
	}
	for *follow {
		time.Sleep(500 * time.Millisecond)
		all, err := c.Entries(memory.EntryFilter{Store: *store, Session: *session})
		if err != nil {
			return err
		}
		var fresh []memory.StoreEntry
		for _, e := range all {
			if !seen[e.ID] {
				seen[e.ID] = true
				fresh = append(fresh, e)
			}
		}
		print(fresh)
	}
	return nil
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

// runPages is spin rules and spin wiki.
func runPages(home string, layer memory.Layer, args []string) error {
	name := string(layer)
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	store := fs.String("store", "", "the store (default: this folder's project, else global)")
	reason := fs.String("reason", "", "edit, retire: why, for the change record")
	full := fs.Bool("full", false, "history: include the text each change replaced")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: spin %[1]s [list] | spin %[1]s show|history SLUG | spin %[1]s edit|retire SLUG --reason WHY\n  edit opens $EDITOR; each takes --store S\n", name)
		fs.PrintDefaults()
	}
	action, slug := "list", ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	if action != "list" {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			fs.Usage()
			os.Exit(2)
		}
		slug, args = args[0], args[1:]
	}
	fs.Parse(args)
	c, err := client(home, "")
	if err != nil {
		return err
	}
	ref, err := memory.ParsePageRef(name + "/" + slug)
	if action != "list" && err != nil {
		return err
	}
	st, err := c.Store(*store)
	if err != nil {
		return err
	}
	switch action {
	case "list":
		if layer == memory.RulesLayer && *store == "" {
			out, err := c.RulesList()
			fmt.Print(out)
			return err
		}
		return listPages(st, layer)
	case "show":
		out, err := c.Zoom(st.Name + "/" + ref.String())
		fmt.Print(out)
		return err
	case "history":
		hist, err := c.History(st.Name, ref)
		fmt.Print(memory.FormatHistory(ref, hist, *full))
		return err
	case "edit", "retire":
		if strings.TrimSpace(*reason) == "" {
			return fmt.Errorf("%s needs --reason, for the change record", action)
		}
		after := ""
		if action == "edit" {
			if after, err = editPage(st, ref); err != nil {
				return err
			}
		}
		ch, err := c.Edit(st.Name, memory.Edit{Target: ref, After: after, Reason: *reason})
		if memory.IsNoChange(err) {
			fmt.Println("Unchanged.")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("Saved %s/%s (%s).\n", st.Name, ch.Target, ch.ID)
		return nil
	}
	fs.Usage()
	os.Exit(2)
	return nil
}

func listPages(st memory.Project, layer memory.Layer) error {
	if layer == memory.RulesLayer {
		rules, err := st.Rules()
		for _, r := range rules {
			fmt.Printf("rules/%s  %s\n", r.Slug, r.Title)
		}
		return err
	}
	pages, err := st.Wiki()
	for _, w := range pages {
		fmt.Printf("wiki/%-28s %-8s %s\n", w.Slug, w.Kind, w.Title)
	}
	if len(pages) == 0 && err == nil {
		fmt.Printf("%s has no wiki pages.\n", st.Name)
	}
	return err
}

// editPage opens the page (or a template for a new one) in $EDITOR and
// returns the text saved. It loops while the text doesn't parse, so a typo
// in the frontmatter isn't lost.
func editPage(st memory.Project, ref memory.PageRef) (string, error) {
	text, err := st.PageText(ref)
	if err != nil {
		return "", err
	}
	if text == "" {
		page := memory.Page{Slug: ref.Slug, Title: string(ref.Slug), Body: "\n"}
		if ref.Layer == memory.RulesLayer {
			text = memory.Rule{Page: page}.Render()
		} else {
			text = memory.WikiPage{Page: page, Kind: memory.KindConcept}.Render()
		}
	}
	f, err := os.CreateTemp("", string(ref.Slug)+"-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		return "", err
	}
	f.Close()
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	for {
		cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", f.Name())
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("editor: %w", err)
		}
		b, err := os.ReadFile(f.Name())
		if err != nil {
			return "", err
		}
		err = memory.CheckPageText(ref, string(b))
		if err == nil {
			return string(b), nil
		}
		fmt.Fprintf(os.Stderr, "%v\nPress Enter to edit again, or Ctrl-C to give up.\n", err)
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return "", fmt.Errorf("the page doesn't parse, and there is no terminal to edit again: %w", err)
		}
	}
}

// runRollup writes the rollups that are due now, as the worker does after
// ingest; it is the worker without the sweep or dream.
func runRollup(home string, args []string) error {
	fs := flag.NewFlagSet("rollup", flag.ExitOnError)
	fs.Parse(args)
	_ = os.Setenv(memory.InnerEnv, "1")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return memory.Work(ctx, home, memory.WorkerOptions{Log: os.Stdout, NoSweep: true, NoDream: true})
}

// runDream is spin dream [--project P] [--dry-run] [--force] | spin dream --due,
// and the commands a dream session runs: spin dream write|retire --session PATH ...
func runDream(home string, args []string) error {
	if len(args) > 0 && (args[0] == "write" || args[0] == "retire") {
		return runDreamEdit(args[0], args[1:])
	}
	fs := flag.NewFlagSet("dream", flag.ExitOnError)
	dir := projectFlag(fs)
	dry := fs.Bool("dry-run", false, "run the session, but have its edits recorded instead of written")
	force := fs.Bool("force", false, "run even if the gate is closed (5 sessions and 24 h since the last dream)")
	due := fs.Bool("due", false, "sweep and ingest, then dream every store whose gate is open (what the schedule runs)")
	fs.Parse(args)
	_ = os.Setenv(memory.InnerEnv, "1")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	spin, err := os.Executable()
	if err != nil {
		return err
	}
	agent := model.AgentFor(model.Dream)
	if *due {
		if err := memory.Work(ctx, home, memory.WorkerOptions{Log: os.Stdout, NoDream: true}); err != nil {
			fmt.Println("worker:", err)
		}
		if n := memory.DreamDueStores(ctx, home, agent, spin, time.Now(), os.Stdout); n == 0 {
			fmt.Println("nothing due")
		}
		return nil
	}
	c, err := client(home, "")
	if err != nil {
		return err
	}
	st, err := c.Store(*dir)
	if err != nil {
		return err
	}
	now := time.Now()
	if !*force && !*dry {
		cp, err := st.LoadCheckpoint()
		if err != nil {
			return err
		}
		es, err := st.Entries()
		if err != nil {
			return err
		}
		if !memory.DreamDue(cp, es, now) {
			return fmt.Errorf("%s isn't due: it dreams after %d sessions and %s; --force runs it anyway, --dry-run shows what it would do", st.Name, memory.DreamMinSessions, memory.DreamEvery)
		}
	}
	mode := memory.DreamApply
	if *dry {
		mode = memory.DreamDryRun
	}
	res, err := st.Dream(ctx, agent, spin, mode, now)
	fmt.Printf("%s:\n%s", st.Name, memory.FormatDream(res))
	return err
}

// runDreamEdit is spin dream write|retire --session PATH TARGET ..., the only
// way a dream session changes a page.
func runDreamEdit(action string, args []string) error {
	fs := flag.NewFlagSet("dream "+action, flag.ExitOnError)
	session := fs.String("session", "", "the dream session file, given in the dream's task")
	title := fs.String("title", "", "write: the page's title")
	kind := fs.String("kind", "", "write, wiki: person, project, link, tool, concept or org")
	evidence := fs.String("evidence", "", "comma-separated ids of the notes the edit rests on")
	reason := fs.String("reason", "", "one line on why")
	body := fs.String("body", "", "write: the whole new body (default: stdin)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: spin dream %s --session PATH rules/SLUG|wiki/SLUG [--title T] [--kind K] --evidence IDS --reason WHY [--body B | < body.md]\n", action)
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() > 1 { // flags after the target
		target := fs.Arg(0)
		fs.Parse(fs.Args()[1:])
		fs.Parse(append([]string{target}, fs.Args()...))
	}
	if fs.NArg() != 1 || *session == "" {
		fs.Usage()
		os.Exit(2)
	}
	e := memory.DreamEdit{Target: fs.Arg(0), Title: *title, Kind: memory.EntityKind(*kind), Reason: *reason, Retire: action == "retire"}
	for _, id := range strings.Split(*evidence, ",") {
		if id = strings.TrimSpace(id); id != "" {
			e.Evidence = append(e.Evidence, memory.EntryID(id))
		}
	}
	if action == "write" {
		e.Body = *body
		if e.Body == "" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			e.Body = string(b)
		}
		if strings.TrimSpace(e.Body) == "" {
			return fmt.Errorf("write needs a body, on stdin or with --body")
		}
	}
	msg, err := memory.ApplyDreamEdit(*session, e, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// runIngest is spin ingest SOURCE [--since TIME] (queues every session the
// source changed since then, then runs the worker) | spin ingest FILE.jsonl.
// Cursors keep either from reading a message twice.
func runIngest(home string, args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	since := fs.String("since", "24h", "SOURCE: sessions changed since a time (RFC 3339), a date (2026-11-01) or a duration ago (48h)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: spin ingest SOURCE [--since T] | spin ingest FILE.jsonl\nsources: "+strings.Join(source.Kinds, ", "))
		fs.PrintDefaults()
	}
	if len(args) == 0 {
		fs.Usage()
		os.Exit(2)
	}
	target := args[0]
	fs.Parse(args[1:])
	_ = os.Setenv(memory.InnerEnv, "1")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if strings.HasSuffix(target, ".jsonl") {
		path, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		s, err := source.Read(ctx, path)
		if err != nil {
			return err
		}
		if err := memory.Enqueue(home, memory.Job{Agent: s.Agent, Session: s.ID, Transcript: path, Cwd: s.Cwd}); err != nil {
			return err
		}
		return memory.Work(ctx, home, memory.WorkerOptions{Log: os.Stdout, NoSweep: true, NoDream: true})
	}
	from, err := parseSince(*since, time.Now())
	if err != nil {
		return err
	}
	srcs, err := source.Load(home)
	if err != nil {
		return err
	}
	for _, src := range srcs {
		if src.Kind() != target {
			continue
		}
		refs, err := src.List(ctx, from, time.Now())
		if err != nil {
			return err
		}
		queued := 0
		for _, r := range refs {
			s, err := src.Read(ctx, r.Loc)
			if err != nil || s.Subagent {
				continue
			}
			if err := memory.Enqueue(home, memory.Job{Agent: r.Agent, Source: target, Session: s.ID, Transcript: r.Loc, Cwd: s.Cwd}); err != nil {
				return err
			}
			queued++
		}
		fmt.Printf("%s: %d sessions changed since %s, queued\n", target, queued, from.Local().Format(time.RFC3339))
		return memory.Work(ctx, home, memory.WorkerOptions{Log: os.Stdout, NoSweep: true, NoDream: true})
	}
	return fmt.Errorf("no source %q in %s; spin sources lists them", target, source.ConfigPath(home))
}

// parseSince reads an RFC 3339 time, a date in the local zone, or a duration ago.
func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: want a time (2026-11-01T09:00:00Z), a date (2026-11-01) or a duration (48h)", s)
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
