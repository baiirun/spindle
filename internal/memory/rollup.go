package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spindle/internal/model"
)

// Rollups summarize the log by time: a session's entries, a day's sessions,
// a week's days. Each is one line, so the session-start index can show a week
// in the space of a gist and zoom opens it into its children. They are a
// cache over the log: a missing or stale rollup is shown as its children
// until the worker writes it again.

// WindowKind is the span a rollup covers.
type WindowKind string

const (
	SessionWindow WindowKind = "s" // Key: the session id
	DayWindow     WindowKind = "d" // Key: 2026-11-08, in the machine's time zone
	WeekWindow    WindowKind = "w" // Key: 2026-W45, ISO week
)

// Window names one rollup. Its text form, "s:<session>", "d:2026-11-08" or
// "w:2026-W45", is the id the index prints and zoom takes.
type Window struct {
	Kind WindowKind
	Key  string
}

// ParseWindow reads a window id. It returns an error for an unknown kind or
// a key that isn't a valid date or ISO week for its kind.
func ParseWindow(id string) (Window, error) {
	kind, key, ok := strings.Cut(id, ":")
	w := Window{Kind: WindowKind(kind), Key: key}
	bad := fmt.Errorf("%q isn't a window: want s:<session>, d:2026-11-08 or w:2026-W45", id)
	if !ok || key == "" {
		return Window{}, bad
	}
	switch w.Kind {
	case SessionWindow:
	case DayWindow:
		if _, err := time.Parse(time.DateOnly, key); err != nil {
			return Window{}, bad
		}
	case WeekWindow:
		var year, week int
		if n, _ := fmt.Sscanf(key, "%04d-W%02d", &year, &week); n != 2 || weekKey(weekStart(year, week, time.UTC)) != key {
			return Window{}, bad
		}
	default:
		return Window{}, bad
	}
	return w, nil
}

func dayKey(t time.Time) string { return t.Format(time.DateOnly) }

func weekKey(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// weekStart is the Monday that starts ISO week w of year y, at midnight in loc.
func weekStart(y, w int, loc *time.Location) time.Time {
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, loc) // always in week 1
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	return monday.AddDate(0, 0, 7*(w-1))
}

// end is when a day or week window closes, in loc.
func (w Window) end(loc *time.Location) time.Time {
	switch w.Kind {
	case DayWindow:
		d, _ := time.ParseInLocation(time.DateOnly, w.Key, loc)
		return d.AddDate(0, 0, 1)
	case WeekWindow:
		var year, week int
		fmt.Sscanf(w.Key, "%04d-W%02d", &year, &week)
		return weekStart(year, week, loc).AddDate(0, 0, 7)
	}
	panic("end of a session window: sessions close by idleness")
}

func (w Window) String() string { return string(w.Kind) + ":" + w.Key }

func (w Window) MarshalText() ([]byte, error) { return []byte(w.String()), nil }

func (w *Window) UnmarshalText(b []byte) error {
	parsed, err := ParseWindow(string(b))
	*w = parsed
	return err
}

// SessionIdle is how long a session must be quiet before it is summarized.
// Hooks report every turn, so a session still going would be summarized again
// and again.
const SessionIdle = 30 * time.Minute

// Rollup is one summary, stored at rollups/<kind>/<key>.json.
type Rollup struct {
	Window Window    `json:"window"`
	Line   Gist      `json:"line"`
	Inputs string    `json:"inputs"` // the digest of the children it summarized; see Node.Digest
	Built  time.Time `json:"built"`
}

// Node is one line of a store's timeline: a week, day, session or entry. The
// timeline is derived from the log and the rollups and never stored.
type Node struct {
	ID       string    // a window id, or an entry id
	At       time.Time // the latest entry under it
	Line     string    // the gist, or the rollup's line; "" when the rollup is missing or stale
	Children []Node    // oldest first; none for an entry
	// Digest identifies everything under the node: each child's id, line
	// and digest. A rollup is fresh only while its Inputs equals this, so an
	// entry added to a summarized session makes the session, its day and its
	// week stale, and a child's new rollup makes its parents stale.
	Digest string
}

// Timeline arranges a store's log into weeks, holding days, holding
// sessions, holding entries; an entry outside any session sits directly
// under its day. A session belongs to the day of its first entry.
//
// Ensures: entries are placed by At, not log order, since adopted entries
// are appended long after they happened.
// Ensures: every entry appears exactly once; weeks oldest first.
// Ensures: a node's Line is its rollup's only when that rollup's Inputs
// equals the node's Digest.
func Timeline(entries []Entry, rollups []Rollup, loc *time.Location) []Node {
	es := slices.Clone(entries)
	slices.SortStableFunc(es, func(a, b Entry) int { return a.At.Compare(b.At) })
	fresh := map[Window]Rollup{}
	for _, r := range rollups {
		fresh[r.Window] = r
	}
	node := func(w Window, children []Node) Node {
		n := Node{ID: w.String(), Children: children}
		parts := make([]string, 0, 3*len(children))
		for _, c := range children {
			parts = append(parts, c.ID, c.Line, c.Digest)
			n.At = maxTime(n.At, c.At)
		}
		n.Digest = digest(parts...)
		if r, ok := fresh[w]; ok && r.Inputs == n.Digest {
			n.Line = string(r.Line)
		}
		return n
	}

	// es is sorted, so days come in order and a session takes its slot in
	// the day of its first entry.
	type slot struct {
		day   string
		index int
	}
	var days []string
	dayChildren := map[string][]Node{}
	sessionAt := map[string]slot{}
	var sessions []string
	sessionLeaves := map[string][]Node{}
	for _, e := range es {
		day := dayKey(e.At.In(loc))
		if _, ok := dayChildren[day]; !ok {
			days = append(days, day)
			dayChildren[day] = []Node{}
		}
		leaf := Node{ID: string(e.ID), At: e.At, Line: string(e.Gist), Digest: digest(string(e.ID), string(e.Gist))}
		if e.Session == "" {
			dayChildren[day] = append(dayChildren[day], leaf)
			continue
		}
		if _, ok := sessionAt[e.Session]; !ok {
			sessionAt[e.Session] = slot{day, len(dayChildren[day])}
			sessions = append(sessions, e.Session)
			dayChildren[day] = append(dayChildren[day], Node{}) // filled once the session is complete
		}
		sessionLeaves[e.Session] = append(sessionLeaves[e.Session], leaf)
	}
	for _, s := range sessions {
		at := sessionAt[s]
		dayChildren[at.day][at.index] = node(Window{SessionWindow, s}, sessionLeaves[s])
	}

	var weeks []string
	weekChildren := map[string][]Node{}
	for _, day := range days {
		date, _ := time.ParseInLocation(time.DateOnly, day, loc)
		week := weekKey(date)
		if _, ok := weekChildren[week]; !ok {
			weeks = append(weeks, week)
		}
		weekChildren[week] = append(weekChildren[week], node(Window{DayWindow, day}, dayChildren[day]))
	}
	out := make([]Node, 0, len(weeks))
	for _, week := range weeks {
		out = append(out, node(Window{WeekWindow, week}, weekChildren[week]))
	}
	return out
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// RollupTask is one rollup the worker should write.
type RollupTask struct {
	Window Window
	Node   Node // its children are what the side model summarizes
}

// DueRollups lists the windows to summarize now, children before parents.
//
// A window is due when it is closed (a session idle for SessionIdle, a day
// or week whose end has passed in loc), every child has a Line, and it has
// no fresh rollup. Its parents come due on a later call, once it has one.
func DueRollups(timeline []Node, now time.Time, loc *time.Location) []RollupTask {
	var out []RollupTask
	var walk func(ns []Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			if len(n.Children) == 0 {
				continue
			}
			walk(n.Children)
			if n.Line != "" || !allLined(n.Children) {
				continue
			}
			w, err := ParseWindow(n.ID)
			if err != nil {
				panic(fmt.Sprintf("timeline node %q isn't a window: %v", n.ID, err))
			}
			closed := false
			if w.Kind == SessionWindow {
				closed = !now.Before(n.At.Add(SessionIdle))
			} else {
				closed = !now.Before(w.end(loc))
			}
			if closed {
				out = append(out, RollupTask{Window: w, Node: n})
			}
		}
	}
	walk(timeline)
	return out
}

func allLined(ns []Node) bool {
	for _, n := range ns {
		if n.Line == "" {
			return false
		}
	}
	return true
}

// Summarize asks the side model for one rollup's line.
//
// Ensures: the result's Inputs is the task node's Digest.
// Ensures: a reply that isn't one valid Gist is an error, and nothing is kept.
func Summarize(ctx context.Context, m model.Model, t RollupTask, now time.Time) (Rollup, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Summarize %s, %d lines, oldest first:\n\n", t.Window, len(t.Node.Children))
	for _, c := range t.Node.Children {
		fmt.Fprintf(&b, "- %s %s\n", c.At.UTC().Format("2006-01-02 15:04"), c.Line)
	}
	var reply struct{ Line string }
	if err := completeJSON(ctx, m, rollupSystem, b.String(), rollupSchema, &reply); err != nil {
		return Rollup{}, fmt.Errorf("rollup %s: %w", t.Window, err)
	}
	line, err := ParseGist(reply.Line)
	if err != nil {
		return Rollup{}, fmt.Errorf("rollup %s: %w", t.Window, err)
	}
	return Rollup{Window: t.Window, Line: line, Inputs: t.Node.Digest, Built: now.UTC()}, nil
}

// Rollups reads every rollup in the store.
func (p Project) Rollups() ([]Rollup, error) {
	if p.Name == "" {
		return nil, nil
	}
	paths, err := filepath.Glob(filepath.Join(p.Dir, "rollups", "*", "*.json"))
	if err != nil {
		return nil, err
	}
	out := make([]Rollup, 0, len(paths))
	for _, path := range paths {
		var r Rollup
		// A rollup that can't be read is a cache miss: its window is
		// summarized again.
		if readJSON(path, &r) == nil && r.Line != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

func (p Project) rollupPath(w Window) string {
	// Session ids are the tools' own; keep them to one path segment.
	key := strings.NewReplacer("/", "_", string(filepath.Separator), "_").Replace(w.Key)
	return filepath.Join(p.Dir, "rollups", string(w.Kind), key+".json")
}

// SaveRollup writes one rollup, replacing the window's old one. Only the
// worker writes rollups, under worker.lock, so this takes no store lock.
func (p Project) SaveRollup(r Rollup) error {
	return writeJSON(p.rollupPath(r.Window), r)
}

// ForgetRollup deletes a window's rollup so the worker writes it again. The
// windows above it go stale on their own, since their digest changes.
func (p Project) ForgetRollup(w Window) error {
	err := os.Remove(p.rollupPath(w))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s has no rollup %s", p.Name, w)
	}
	return err
}
