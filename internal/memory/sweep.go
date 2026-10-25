package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"spindle/internal/source"
)

// SweepSettle is how long a session must be quiet before a sweep reads it. A
// session that is still going is left to its hooks, which queue it at the end
// of each turn; the sweep catches what no hook reported.
const SweepSettle = 10 * time.Minute

// Mark is how far one source has been swept.
type Mark struct {
	Swept  time.Time `json:"swept"`         // sessions changed up to here have been looked at
	At     time.Time `json:"at"`            // when the last sweep ran
	Found  int       `json:"found"`         // sessions it found changed
	Queued int       `json:"queued"`        // of those, sessions with unread messages
	Err    string    `json:"err,omitempty"` // why the last sweep failed
}

func marksPath(home string) string { return filepath.Join(home, "sweeps.json") }

// Marks reads how far each source has been swept, by kind.
func Marks(home string) (map[string]*Mark, error) {
	m := map[string]*Mark{}
	err := readJSON(marksPath(home), &m)
	return m, err
}

// Sweep asks every source for the sessions that changed since its last sweep
// and queues the ones with messages extraction hasn't read. The first sweep
// of a source only sets its mark, so adding a source never reads its whole
// history. Only the worker sweeps, under worker.lock, so the marks have one
// writer. A source that fails keeps its mark and doesn't stop the others.
func Sweep(ctx context.Context, home string, srcs []source.Source, now time.Time) error {
	marks, err := Marks(home)
	if err != nil {
		return err
	}
	cursors, err := Cursors(home)
	if err != nil {
		return err
	}
	until := now.Add(-SweepSettle).UTC()
	log := Project{Home: home}
	for _, src := range srcs {
		if ctx.Err() != nil {
			break
		}
		kind := src.Kind()
		m := marks[kind]
		if m == nil {
			m = &Mark{}
			marks[kind] = m
		}
		m.At = now.UTC()
		// A source added (or added again) since its mark starts afresh.
		var added time.Time
		if a, ok := src.(interface{ Added() time.Time }); ok {
			added = a.Added().UTC()
		}
		if m.Swept.IsZero() || m.Swept.Before(added) {
			m.Swept, m.Found, m.Queued, m.Err = until, 0, 0, ""
			if !added.IsZero() {
				m.Swept = added // sessions that changed after it was added still count
			}
			log.Log(Event{At: now, Kind: "sweep", Name: kind, Note: "first sweep: reading sessions that change from now on"})
			continue
		}
		if !until.After(m.Swept) {
			continue // added within the last few minutes: nothing has settled since
		}
		refs, err := src.List(ctx, m.Swept, until)
		if err != nil {
			m.Err = err.Error()
			log.Log(Event{At: now, Kind: "sweep", Name: kind, Err: m.Err})
			continue
		}
		queued := 0
		var qerr error
		for _, r := range refs {
			// A session read after its last change has nothing new.
			if c := cursors[r.Loc]; c != nil && !r.Updated.After(c.At) {
				continue
			}
			s, err := src.Read(ctx, r.Loc)
			if err != nil || s.Subagent || len(s.Since(lineOf(cursors[r.Loc]))) == 0 {
				continue
			}
			if qerr = Enqueue(home, Job{Agent: r.Agent, Source: kind, Session: s.ID, Transcript: r.Loc, Cwd: s.Cwd}); qerr != nil {
				break
			}
			queued++
		}
		if qerr != nil {
			m.Err = qerr.Error()
			log.Log(Event{At: now, Kind: "sweep", Name: kind, Err: m.Err})
			continue
		}
		m.Swept, m.Found, m.Queued, m.Err = until, len(refs), queued, ""
		if len(refs) > 0 {
			log.Log(Event{At: now, Kind: "sweep", Name: kind, Note: fmt.Sprintf("%d changed, %d with unread messages queued", len(refs), queued)})
		}
	}
	return writeJSON(marksPath(home), marks)
}

func lineOf(c *Cursor) int {
	if c == nil {
		return 0
	}
	return c.Line
}

// SourceInfo is one configured source and its last sweep.
type SourceInfo struct {
	Kind  string
	Where string // what it reads, such as a folder
	Mark  Mark   // zero if it has never been swept
}

func sourceInfo(src source.Source, marks map[string]*Mark) SourceInfo {
	s := SourceInfo{Kind: src.Kind()}
	if f, ok := src.(source.Files); ok {
		s.Where = f.Dir()
	}
	if m := marks[s.Kind]; m != nil {
		s.Mark = *m
	}
	return s
}

// FormatSources renders the sources for spin sources and memory_sources.
func FormatSources(path string, infos []SourceInfo, cfgErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sessions come from the sources in %s.\n", path)
	if cfgErr != nil {
		fmt.Fprintf(&b, "Problem with the config: %v\n", cfgErr)
	}
	if len(infos) == 0 {
		b.WriteString("No sources: only sessions whose hooks queue them are read.\n")
		return b.String()
	}
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].Kind < infos[j].Kind })
	for _, s := range infos {
		m := s.Mark
		fmt.Fprintf(&b, "\n%s", s.Kind)
		if s.Where != "" {
			fmt.Fprintf(&b, " (%s)", s.Where)
		}
		b.WriteString("\n")
		switch {
		case m.At.IsZero():
			b.WriteString("  not swept yet\n")
		case m.Err != "":
			fmt.Fprintf(&b, "  last sweep %s failed: %s\n", m.At.Local().Format("2006-01-02 15:04"), m.Err)
		default:
			fmt.Fprintf(&b, "  last sweep %s: %d sessions changed, %d queued for extraction\n", m.At.Local().Format("2006-01-02 15:04"), m.Found, m.Queued)
		}
		if !m.Swept.IsZero() {
			fmt.Fprintf(&b, "  read up to sessions that changed by %s\n", m.Swept.Local().Format("2006-01-02 15:04"))
		}
	}
	return b.String()
}
