package memory

import (
	"context"
	"time"

	"spindle/internal/model"
)

// Dream is one pass over one store: it reads the log entries since its
// checkpoint, the rollups over them and the store's pages, and edits rules
// and wiki pages. Planning and applying are separate steps, so a dry run is
// a plan that is printed and never applied.

// The gate: a store is due when this many distinct sessions have added
// entries since its checkpoint and its last dream is this old.
const (
	DreamMinSessions = 5
	DreamEvery       = 24 * time.Hour
)

// Checkpoint is dream.json: how far dream has read the store's log. Only
// ApplyDream writes it, under the store's dream.lock.
type Checkpoint struct {
	Read int       `json:"read"` // entries read, a prefix of the log
	Last EntryID   `json:"last"` // the id of entry Read-1, to catch a log that changed under it
	At   time.Time `json:"at"`   // when that dream ran; zero for never
}

// DreamDue reports whether the gate is open.
//
// Requires: cp.Read <= len(entries).
func DreamDue(cp Checkpoint, entries []Entry, now time.Time) bool {
	panic("unimplemented")
}

// DreamInput is everything a dream reads. It is plain data, so planning can
// be run, inspected and tested without the store.
type DreamInput struct {
	Store        string // the store's name, for the prompt
	Fresh        []Entry
	Rollups      []Rollup // those over the days the fresh entries fall in
	Rules        []Rule
	Wiki         []WikiPage
	Instructions string // CLAUDE.md and AGENTS.md from the project's first linked folder
	Through      Checkpoint
}

// LoadDreamInput gathers a store's DreamInput.
//
// Ensures: an error if the checkpoint's Last isn't the id at Read-1, since
// the log then isn't the one dream read.
func (p Project) LoadDreamInput(now time.Time) (DreamInput, error) {
	panic("unimplemented")
}

// Rejection is an edit the plan dropped, and why.
type Rejection struct {
	Edit Edit
	Why  string
}

// DreamPlan is what a dream would do.
type DreamPlan struct {
	Edits    []Edit
	Rejected []Rejection
	Summary  string
	Through  Checkpoint // the checkpoint to write once the edits are applied
}

// PlanDream asks the side model for edits and checks them with CheckEdits.
// The model reads every rule, then the wiki as slugs and titles, and asks
// for the pages it needs in full before it writes.
//
// Ensures: an error only when the model fails or its reply isn't the schema;
// bad individual edits become Rejections.
// Must not: touch the store. It has no way to: it gets data, not a Project.
func PlanDream(ctx context.Context, m model.Model, in DreamInput) (DreamPlan, error) {
	panic("unimplemented")
}

// CheckEdits splits proposed edits into those to apply and those to reject.
//
// An edit is rejected when its Reason is empty; its After doesn't parse as a
// page of its layer; it creates a page that exists or retires or changes one
// that doesn't; it cites an entry that is neither in in.Fresh nor already
// cited by its page.
func CheckEdits(in DreamInput, edits []Edit) (ok []Edit, rejected []Rejection) {
	panic("unimplemented")
}

// Dream loads, plans and applies one dream under the store's dream.lock. A
// dry run calls LoadDreamInput and PlanDream itself and stops there.
//
// Ensures: returns errLocked, doing nothing, if another dream holds dream.lock.
// Ensures: the checkpoint moves to plan.Through only after every edit is
// written. A crash before that reruns the same entries, and ApplyEdits skips
// what is already there.
func (p Project) Dream(ctx context.Context, m model.Model, now time.Time) (DreamPlan, []Change, error) {
	panic("unimplemented")
}
