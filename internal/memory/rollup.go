package memory

import (
	"context"
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
	panic("unimplemented")
}

func (w Window) String() string { return string(w.Kind) + ":" + w.Key }

func (w Window) MarshalText() ([]byte, error) { return []byte(w.String()), nil }

func (w *Window) UnmarshalText(b []byte) error {
	panic("unimplemented")
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
	// Digest identifies the children's ids and lines. A rollup is fresh only
	// while its Inputs equals this, so an entry added to a summarized session
	// makes the session, its day and its week stale.
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
	panic("unimplemented")
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
	panic("unimplemented")
}

// Summarize asks the side model for one rollup's line.
//
// Ensures: the result's Inputs is the task node's Digest.
// Ensures: a reply that isn't one valid Gist is an error, and nothing is kept.
func Summarize(ctx context.Context, m model.Model, t RollupTask, now time.Time) (Rollup, error) {
	panic("unimplemented")
}

// Rollups reads every rollup in the store.
func (p Project) Rollups() ([]Rollup, error) {
	panic("unimplemented")
}

// SaveRollup writes one rollup, replacing the window's old one. Only the
// worker writes rollups, under worker.lock, so this takes no store lock.
func (p Project) SaveRollup(r Rollup) error {
	panic("unimplemented")
}

// ForgetRollup deletes a window's rollup so the worker writes it again. The
// windows above it go stale on their own, since their digest changes.
func (p Project) ForgetRollup(w Window) error {
	panic("unimplemented")
}
