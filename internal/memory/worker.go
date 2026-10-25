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
	"time"

	"spindle/internal/model"
)

// Job asks the worker to run extraction for one transcript. Hooks only write
// job files; every model call happens in the worker, so a hook never waits.
type Job struct {
	Agent      string    `json:"agent"`
	Session    string    `json:"session"`
	Transcript string    `json:"transcript"`
	Cwd        string    `json:"cwd"`
	Project    string    `json:"project,omitempty"` // the session's project when queued
	Root       string    `json:"root,omitempty"`    // older jobs: the repository root
	At         time.Time `json:"at"`
}

func queueDir(home string) string { return filepath.Join(home, "queue") }

// Enqueue writes a job file.
func Enqueue(home string, j Job) error {
	if j.At.IsZero() {
		j.At = time.Now().UTC()
	}
	var r [4]byte
	rand.Read(r[:])
	return writeJSON(filepath.Join(queueDir(home), fmt.Sprintf("%d-%s.json", j.At.UnixNano(), hex.EncodeToString(r[:]))), j)
}

func pending(home string) []string {
	paths, _ := filepath.Glob(filepath.Join(queueDir(home), "*.json"))
	sort.Strings(paths)
	return paths
}

// Models picks the model for a role given the agent that queued the work.
type Models func(agent string, role model.Role) model.Model

// WorkerOptions configures a worker run.
type WorkerOptions struct {
	Models  Models
	Log     io.Writer
	NoDream bool
	Now     func() time.Time
}

// Work drains the queue and exits. One worker runs at a time; another finds
// the lock held and leaves, and the running one picks up its jobs because it
// reads the queue until it is empty.
func Work(ctx context.Context, home string, o WorkerOptions) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Models == nil {
		o.Models = func(agent string, role model.Role) model.Model { return model.For(agent, role) }
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
		if err != nil || len(pending(home)) == 0 {
			return err
		}
	}
}

func drain(ctx context.Context, home string, o WorkerOptions) error {
	agent := ""
	for {
		paths := pending(home)
		if len(paths) == 0 {
			break
		}
		// Many Stops for one transcript are one pass.
		jobs := map[string]Job{}
		var order []string
		for _, path := range paths {
			var j Job
			if err := readJSON(path, &j); err == nil && j.Transcript != "" {
				if j.Cwd == "" {
					j.Cwd = j.Root
				}
				if _, ok := jobs[j.Transcript]; !ok {
					order = append(order, j.Transcript)
				}
				jobs[j.Transcript] = j
			}
			os.Remove(path)
		}
		for _, tp := range order {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			j := jobs[tp]
			agent = j.Agent
			p, err := jobContext(home, j)
			if err != nil {
				fmt.Fprintf(o.Log, "%s extract %s: %v\n", stamp(o.Now()), tp, err)
				continue
			}
			res, err := Extract(ctx, p, tp, o.Models(j.Agent, model.Extract), o.Now())
			switch {
			case err != nil:
				// The cursor did not move, so the next Stop retries.
				fmt.Fprintf(o.Log, "%s extract %s: %v\n", stamp(o.Now()), tp, err)
			case res.Skip == "":
				fmt.Fprintf(o.Log, "%s extract %s %s: %d written, %d deleted, %d skipped\n", stamp(o.Now()), orDash(p.Name), filepath.Base(tp), res.Written, res.Deleted, res.Result.Skipped)
			}
		}
	}
	if o.NoDream || agent == "" {
		return nil
	}
	DreamDueStores(ctx, home, o.Models(agent, model.Dream), o.Now(), o.Log)
	return nil
}

// DreamDueStores dreams every store whose gate is open. The scheduled run
// (spin dream --due) and the worker after each drain both call it; the gate
// and the dream lock keep them from doubling up. It returns how many ran.
func DreamDueStores(ctx context.Context, home string, m model.Model, now time.Time, log io.Writer) int {
	n := 0
	all, _ := stores(home)
	for _, s := range all {
		if ctx.Err() != nil {
			break
		}
		state, err := s.LoadState()
		if err != nil || !DreamDue(state, now) {
			continue
		}
		n++
		res, err := Dream(ctx, s, m, now)
		if err != nil {
			fmt.Fprintf(log, "%s dream %s: %v\n", stamp(now), s.Name, err)
			continue
		}
		fmt.Fprintf(log, "%s dream %s: %d written, %d deleted. %s\n", stamp(now), s.Name, res.Written, res.Deleted, res.Summary)
	}
	return n
}

// jobContext is the session context a job was queued in.
func jobContext(home string, j Job) (Project, error) {
	if j.Project != "" {
		p := Store(home, j.Project)
		p.Cwd = j.Cwd
		return p, nil
	}
	if j.Cwd == "" {
		return Project{Home: home}, nil
	}
	return Hint(home, j.Cwd)
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
