package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"spindle/internal/model"
	"spindle/internal/source"
)

// Job asks the worker to ingest one session. Hooks write one at
// the end of each turn, and a sweep of the sources writes them for sessions
// no hook reported. Every model call happens in the worker, so a hook never
// waits.
type Job struct {
	Agent      string    `json:"agent"`
	Source     string    `json:"source,omitempty"` // the source that found it; empty: a hook queued it
	Session    string    `json:"session"`
	Transcript string    `json:"transcript"` // where the session's source reads it
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
	_, _ = rand.Read(r[:])
	return writeJSON(filepath.Join(queueDir(home), fmt.Sprintf("%d-%s.json", j.At.UnixNano(), hex.EncodeToString(r[:]))), j)
}

func pending(home string) []string {
	paths, _ := filepath.Glob(filepath.Join(queueDir(home), "*.json"))
	sort.Strings(paths)
	return paths
}

// Models picks the model for a role.
type Models func(role model.Role) model.Model

// Agents picks the agent for a role that runs as a session.
type Agents func(role model.Role) model.Agent

// WorkerOptions configures a worker run.
type WorkerOptions struct {
	Models  Models
	Agents  Agents
	Spin    string // the spin binary dream sessions run their commands with; default: this one
	Log     io.Writer
	NoDream bool // skip the dream gate check; rollups still run
	NoSweep bool
	Now     func() time.Time
	// Sources are swept before the queue is drained; nil reads sources.json.
	Sources func() ([]source.Source, error)
}

// Work sweeps the sources, drains the queue and exits. One worker runs at a
// time; another finds the lock held and leaves, and the running one picks up
// its jobs because it reads the queue until it is empty.
func Work(ctx context.Context, home string, o WorkerOptions) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Models == nil {
		o.Models = model.For
	}
	if o.Agents == nil {
		o.Agents = model.AgentFor
	}
	if o.Spin == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		o.Spin = exe
	}
	if o.Sources == nil {
		o.Sources = func() ([]source.Source, error) { return source.Load(home) }
	}
	if err := os.MkdirAll(queueDir(home), 0o755); err != nil {
		return err
	}
	swept := o.NoSweep
	for {
		unlock, err := lockFile(filepath.Join(home, "worker.lock"), false)
		if err == errLocked {
			return nil
		}
		if err != nil {
			return err
		}
		if !swept {
			swept = true
			sweep(ctx, home, o)
		}
		err = drain(ctx, home, o)
		unlock()
		if err != nil || len(pending(home)) == 0 {
			return err
		}
	}
}

// drain ingests every queued job, then writes the rollups that came due
// and dreams the stores whose gate is open.
//
// Ensures: several jobs for one transcript make one Ingest pass.
// Ensures: a failed job is logged and its cursor stays, so the next Stop
// or sweep retries it; it doesn't stop the other jobs.
func drain(ctx context.Context, home string, o WorkerOptions) error {
	for {
		paths := pending(home)
		if len(paths) == 0 {
			break
		}
		jobs := map[string]Job{}
		var order []string
		for _, path := range paths {
			var j Job
			if err := readJSON(path, &j); err == nil && j.Transcript != "" {
				if _, ok := jobs[j.Transcript]; !ok {
					order = append(order, j.Transcript)
				}
				jobs[j.Transcript] = j
			}
			os.Remove(path)
		}
		for _, loc := range order {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := ingestJob(ctx, home, jobs[loc], o); err != nil {
				// The cursor didn't move, so the next Stop or sweep retries.
				fmt.Fprintf(o.Log, "%s ingest %s: %v\n", stamp(o.Now()), loc, err)
			}
		}
	}
	RollUp(ctx, home, o.Models(model.Rollup), o.Now(), time.Local, o.Log)
	if !o.NoDream {
		DreamDueStores(ctx, home, o.Agents(model.Dream), o.Spin, o.Now(), o.Log)
	}
	return nil
}

func ingestJob(ctx context.Context, home string, j Job, o WorkerOptions) error {
	p, err := jobContext(home, j)
	if err != nil {
		return err
	}
	s, err := source.Read(ctx, j.Transcript)
	if err != nil {
		return err
	}
	res, err := Ingest(ctx, p, s, o.Models(model.Ingest), o.Now())
	if err == nil && res.Skip == "" {
		fmt.Fprintf(o.Log, "%s ingest %s %s: %d project, %d global, %d unfiled\n", stamp(o.Now()), orDash(p.Name), filepath.Base(j.Transcript), res.Project, res.Global, res.Unfiled)
	}
	return err
}

func sweep(ctx context.Context, home string, o WorkerOptions) {
	srcs, err := o.Sources()
	if err != nil {
		fmt.Fprintf(o.Log, "%s sources: %v\n", stamp(o.Now()), err)
		Project{Home: home}.Log(Event{Kind: "sweep", Err: err.Error()})
	}
	if err := Sweep(ctx, home, srcs, o.Now()); err != nil {
		fmt.Fprintf(o.Log, "%s sweep: %v\n", stamp(o.Now()), err)
	}
}

// RollupsPerRun caps the rollups one run writes, newest first, so a long
// backlog (after a migration, or a side model that was down) is worked off
// over several runs instead of holding the worker for an hour.
const RollupsPerRun = 12

// RollUp writes due rollups in global and every project, newest first, a
// round at a time (sessions, then days, then weeks) until none is due or
// RollupsPerRun are written. Unfiled has no timeline in any index, so it
// gets none.
//
// Requires: the caller holds worker.lock, the rollups' only writer.
// Ensures: a failed Summarize is logged and leaves that window due; the
// rest still run. It returns how many rollups it wrote.
func RollUp(ctx context.Context, home string, m model.Model, now time.Time, loc *time.Location, log io.Writer) int {
	all, err := stores(home)
	if err != nil {
		fmt.Fprintf(log, "%s rollup: %v\n", stamp(now), err)
	}
	written, failed := 0, map[Window]bool{}
	for round := 0; round < 3 && written < RollupsPerRun; round++ {
		progress := false
		for _, st := range all {
			es, err := st.Entries()
			if err != nil {
				fmt.Fprintf(log, "%s rollup %s: %v\n", stamp(now), st.Name, err)
				continue
			}
			rs, _ := st.Rollups()
			due := DueRollups(Timeline(es, rs, loc), now, loc)
			slices.SortStableFunc(due, func(a, b RollupTask) int { return b.Node.At.Compare(a.Node.At) })
			for _, t := range due {
				if ctx.Err() != nil || written >= RollupsPerRun {
					return written
				}
				if failed[t.Window] {
					continue
				}
				began := time.Now()
				r, err := Summarize(ctx, m, t, now)
				if err == nil {
					err = st.SaveRollup(r)
				}
				st.Log(Event{At: now, Kind: "rollup", Project: st.Name, Name: t.Window.String(), Ms: time.Since(began).Milliseconds(), Note: string(r.Line), Err: errString(err)})
				if err != nil {
					failed[t.Window] = true
					fmt.Fprintf(log, "%s rollup %s %s: %v\n", stamp(now), st.Name, t.Window, err)
					continue
				}
				written++
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	return written
}

// DreamDueStores dreams every store whose gate is open. The scheduled run
// (spin dream --due) and the worker after each drain both call it; the gate
// and the dream lock keep them from doubling up. It returns how many ran.
func DreamDueStores(ctx context.Context, home string, agent model.Agent, spin string, now time.Time, log io.Writer) int {
	all, err := stores(home)
	if err != nil {
		fmt.Fprintf(log, "%s dream: %v\n", stamp(now), err)
	}
	n := 0
	for _, st := range all {
		if ctx.Err() != nil {
			break
		}
		cp, err := st.LoadCheckpoint()
		if err != nil {
			fmt.Fprintf(log, "%s dream %s: %v\n", stamp(now), st.Name, err)
			continue
		}
		es, err := st.Entries()
		if err != nil || cp.Read > len(es) || !DreamDue(cp, es, now) {
			continue
		}
		res, err := st.Dream(ctx, agent, spin, DreamApply, now)
		if errors.Is(err, errLocked) {
			continue
		}
		n++
		if err != nil {
			fmt.Fprintf(log, "%s dream %s: %v\n", stamp(now), st.Name, err)
			continue
		}
		fmt.Fprintf(log, "%s dream %s: %d changed, %d rejected. %s\n", stamp(now), st.Name, len(res.Changes), len(res.Rejected), res.Summary)
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
