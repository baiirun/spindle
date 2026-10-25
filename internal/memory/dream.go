package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"spindle/internal/model"
	"spindle/internal/transcript"
)

// Dreaming is Claude Code's auto_dream: at most once a day, after enough
// sessions, a pass over all of a project's memories that merges duplicates,
// fixes what recent sessions contradicted, and prunes. Neither tool has a
// scheduled hook, so the worker checks the gate after each drain.

const (
	DreamEvery    = 24 * time.Hour
	DreamSessions = 5
	dreamSignal   = 120_000 // characters of recent messages given to the dream
)

// DreamDue reports whether the gate is open.
func DreamDue(s State, now time.Time) bool {
	return len(s.Sessions) >= DreamSessions && (s.LastDream.IsZero() || now.Sub(s.LastDream) >= DreamEvery)
}

// DreamResult says what a dream did.
type DreamResult struct {
	Result
	Summary string
}

// ErrDreaming means another dream is running for the store.
var ErrDreaming = errors.New("a dream is already running for this project")

// Dream runs one pass over one store and resets its gate. One dream runs at
// a time per store.
func Dream(ctx context.Context, p Project, m model.Model, now time.Time) (DreamResult, error) {
	if err := p.Init(); err != nil {
		return DreamResult{}, err
	}
	unlock, err := lockFile(filepath.Join(p.Dir, "dream.lock"), false)
	if err == errLocked {
		return DreamResult{}, ErrDreaming
	}
	if err != nil {
		return DreamResult{}, err
	}
	defer unlock()
	start := time.Now()
	res, err := dream(ctx, p, m, now)
	p.Log(Event{Kind: "dream", Files: res.Changed, Ms: time.Since(start).Milliseconds(), Note: res.Summary, Err: errString(err)})
	return res, err
}

func dream(ctx context.Context, p Project, m model.Model, now time.Time) (DreamResult, error) {
	var res DreamResult
	if err := p.Init(); err != nil {
		return res, err
	}
	state, err := p.LoadState()
	if err != nil {
		return res, err
	}
	mems, err := p.Load()
	if err != nil {
		return res, err
	}
	var b strings.Builder
	b.WriteString("## Memory files\n\n")
	if len(mems) == 0 {
		b.WriteString("(none)\n")
	}
	for _, mem := range mems {
		fmt.Fprintf(&b, "<file name=%q modified=%q>\n%s</file>\n\n", mem.File(), mem.Modified.UTC().Format(time.RFC3339), mem.Render())
	}
	b.WriteString("## Instruction files\n\n")
	found := false
	var folder string
	if fs := p.Meta().Folders; len(fs) > 0 {
		folder = fs[0]
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", filepath.Join(".claude", "CLAUDE.md")} {
		if folder == "" {
			break
		}
		if text, err := os.ReadFile(filepath.Join(folder, name)); err == nil {
			found = true
			fmt.Fprintf(&b, "<file name=%q>\n%s\n</file>\n\n", name, clipRunes(string(text), 20_000))
		}
	}
	if !found {
		b.WriteString("(none)\n\n")
	}
	if p.IsGlobal() {
		b.WriteString(globalSize(p))
	}
	fmt.Fprintf(&b, "## Messages from the %d sessions since the last dream\n\n%s", len(state.Sessions), recentSignal(state, dreamSignal))

	var out struct {
		Ops     []Op   `json:"ops"`
		Summary string `json:"summary"`
	}
	if err := completeJSON(ctx, m, dreamSystem, b.String(), dreamSchema, &out); err != nil {
		return res, fmt.Errorf("dream: %w", err)
	}
	unlock, err := p.Lock()
	if err != nil {
		return res, err
	}
	defer unlock()
	// The dream sees one store, so its names are bare. A name prefixed with
	// another store points outside what it saw; skip it rather than strip it.
	var ops []Op
	skipped := 0
	for _, o := range out.Ops {
		store, name := splitRef(o.Name)
		if store != "" && store != p.Name {
			skipped++
			continue
		}
		o.Name = name
		ops = append(ops, o)
	}
	if res.Result, err = p.Apply(ops, "dream", "", now); err != nil {
		return res, err
	}
	res.Skipped += skipped
	res.Summary = out.Summary
	seen := state.Sessions
	if state, err = p.LoadState(); err != nil {
		return res, err
	}
	// Clear only the sessions this dream read; ones marked while it waited on
	// the model count toward the next dream.
	for id := range seen {
		delete(state.Sessions, id)
	}
	state.LastDream = now.UTC()
	if err := p.SaveState(state); err != nil {
		return res, err
	}
	return res, appendJSONL(filepath.Join(p.Dir, "dreams.jsonl"), []map[string]any{{"at": now.UTC(), "summary": out.Summary, "written": res.Written, "deleted": res.Deleted}})
}

// globalSize tells the global dream how much room global's index has at
// session start, so that it keeps global small enough to load whole.
func globalSize(g Project) string {
	room := GlobalRoom()
	sec := indexSection(g, "", GlobalShare)
	n := sec.cost(sec.items)
	text := fmt.Sprintf("## Size\n\nThe global index is loaded at the start of every session in every project, in about %d characters. It is now %d characters.", room, n)
	if n > room {
		text += " That is over: merge overlapping memories and shorten descriptions until it fits. Lines past the limit are left out of session start, newest directives kept first."
	} else {
		text += " Keep it within that."
	}
	return text + "\n\n"
}

// recentSignal renders the messages since the last dream, newest sessions
// first, within n characters.
func recentSignal(s State, n int) string {
	type sess struct {
		id   string
		msgs []transcript.Message
	}
	var all []sess
	for id, path := range s.Sessions {
		t, err := transcript.Read(path)
		if err != nil {
			continue
		}
		var msgs []transcript.Message
		for _, m := range t.Messages {
			if m.Time.After(s.LastDream) {
				msgs = append(msgs, m)
			}
		}
		if len(msgs) > 0 {
			all = append(all, sess{id, msgs})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].msgs[len(all[i].msgs)-1].Time.After(all[j].msgs[len(all[j].msgs)-1].Time)
	})
	var b strings.Builder
	for _, x := range all {
		block := fmt.Sprintf("### Session %s\n\n%s", x.id, transcript.Render(x.msgs, 2_000))
		if b.Len()+len(block) > n {
			left := n - b.Len()
			if left < 2_000 {
				break
			}
			block = "…" + block[len(block)-left:] // keep the session's latest messages
		}
		b.WriteString(block)
	}
	return orNone(b.String())
}
