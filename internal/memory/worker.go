package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Job is one unit of work a hook hands the worker. Hooks only write a job
// file; every model call happens in the worker, so no hook ever waits on one.
type Job struct {
	Event      string `json:"event"`
	Agent      string `json:"agent"`
	Session    string `json:"session"`
	Transcript string `json:"transcript"`
	Root       string `json:"root"`  // project root
	Force      bool   `json:"force"` // fold whatever is new, below the threshold too
	// Flush: fold every other transcript of the project that has unfolded
	// conversation. Set when a session starts, so a previous session's last
	// turns are not left behind.
	Flush bool      `json:"flush"`
	At    time.Time `json:"at"`
}

func queueDir(home string) string { return filepath.Join(home, "queue") }

// Enqueue writes a job file. It is the only thing the Stop, PreCompact and
// SessionEnd hooks do, so it has to be fast.
func Enqueue(home string, j Job) error {
	if j.At.IsZero() {
		j.At = time.Now().UTC()
	}
	var r [4]byte
	rand.Read(r[:])
	name := fmt.Sprintf("%d-%s.json", j.At.UnixNano(), hex.EncodeToString(r[:]))
	return writeJSON(filepath.Join(queueDir(home), name), j)
}

func pendingJobs(home string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(queueDir(home), "*.json"))
	sort.Strings(paths)
	return paths, err
}

// WorkerOptions configures a worker run.
type WorkerOptions struct {
	Model         Model
	Corpus        string
	Log           io.Writer
	DreamEvery    time.Duration
	DreamSessions int
	NoDream       bool
	Now           func() time.Time
}

// Work drains the queue and exits. Only one worker runs at a time: a second
// one finds the lock held and returns, and the running one picks up its jobs
// because it re-reads the queue until it is empty.
func Work(ctx context.Context, home string, o WorkerOptions) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DreamEvery == 0 {
		o.DreamEvery = DreamEvery
	}
	if o.DreamSessions == 0 {
		o.DreamSessions = DreamSessions
	}
	if err := os.MkdirAll(queueDir(home), 0o755); err != nil {
		return err
	}
	for {
		unlock, err := lockFile(filepath.Join(home, "worker.lock"), false)
		if err == errLocked {
			return nil
		}
		if err != nil {
			return err
		}
		err = drain(ctx, home, o)
		unlock()
		if err != nil {
			return err
		}
		// A job written after the last read but before the unlock would wait
		// for the next hook; look once more now that the lock is free.
		if left, _ := pendingJobs(home); len(left) == 0 {
			return nil
		}
	}
}

type target struct {
	project    Project
	transcript string
	agent      string
	force      bool
}

func drain(ctx context.Context, home string, o WorkerOptions) error {
	touched := map[string]Project{}
	for {
		paths, err := pendingJobs(home)
		if err != nil || len(paths) == 0 {
			break
		}
		// Coalesce: many Stops for one transcript are one pass.
		targets := map[string]*target{}
		var order []string
		addTarget := func(p Project, transcript, agent string, force bool) {
			t := targets[transcript]
			if t == nil {
				t = &target{project: p, transcript: transcript, agent: agent}
				targets[transcript] = t
				order = append(order, transcript)
			}
			t.force = t.force || force
		}
		for _, path := range paths {
			var j Job
			if err := readJSON(path, &j); err != nil || j.Root == "" {
				fmt.Fprintf(o.Log, "%s drop bad job %s: %v\n", stamp(o.Now()), filepath.Base(path), err)
				os.Remove(path)
				continue
			}
			p := ProjectAt(home, j.Root)
			touched[p.Slug] = p
			if j.Transcript != "" {
				addTarget(p, j.Transcript, j.Agent, j.Force)
			}
			if j.Flush {
				state, err := p.LoadState()
				if err == nil {
					for tp, c := range state.Cursors {
						if tp != j.Transcript && c.Pending > 0 {
							addTarget(p, tp, c.Agent, true)
						}
					}
				}
			}
			os.Remove(path)
		}
		for _, tp := range order {
			t := targets[tp]
			if ctx.Err() != nil {
				return ctx.Err()
			}
			res, err := Extract(ctx, t.project, t.transcript, t.agent, t.force, ExtractOptions{Model: o.Model, Corpus: o.Corpus, Now: o.Now})
			if err != nil {
				// Keep going: one bad transcript must not block the others. The
				// cursor did not move, so the next Stop retries it.
				fmt.Fprintf(o.Log, "%s extract %s: %v\n", stamp(o.Now()), t.transcript, err)
				continue
			}
			if res.Folded {
				fmt.Fprintf(o.Log, "%s folded %s (%s): %d chars, %d user msgs, record +%d ~%d -%d (%d skipped, %d bad cites)\n",
					stamp(o.Now()), t.project.Name(), filepath.Base(t.transcript), res.NewChars, res.Users,
					res.Record.Added, res.Record.Replaced, res.Record.Dropped, res.Record.Skipped, res.Record.BadCites)
			}
		}
	}
	if o.NoDream {
		return nil
	}
	for _, p := range touched {
		state, err := p.LoadState()
		if err != nil || !DreamDue(state, o.Now(), o.DreamEvery, o.DreamSessions) {
			continue
		}
		res, err := Dream(ctx, p, o.Model, o.Now())
		if err != nil {
			fmt.Fprintf(o.Log, "%s dream %s: %v\n", stamp(o.Now()), p.Name(), err)
			continue
		}
		fmt.Fprintf(o.Log, "%s dreamed %s: ~%d -%d (%d skipped)\n", stamp(o.Now()), p.Name(), res.Replaced, res.Dropped, res.Skipped)
	}
	return nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Projects lists the project buckets under home.
func Projects(home string) ([]Project, error) {
	dirs, err := filepath.Glob(filepath.Join(home, "projects", "*", "project.json"))
	if err != nil {
		return nil, err
	}
	var out []Project
	for _, d := range dirs {
		var meta struct {
			Root string `json:"root"`
		}
		if readJSON(d, &meta) != nil || meta.Root == "" {
			continue
		}
		out = append(out, Project{Root: meta.Root, Slug: filepath.Base(filepath.Dir(d)), Dir: filepath.Dir(d), Home: home})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Root) < strings.ToLower(out[j].Root) })
	return out, nil
}
