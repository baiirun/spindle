package memory

import (
	"time"
)

// MigrateV1 turns each store's spindle v1 memories into the layers, next to
// the v1 files, which it only reads:
//
//	every v1 changes.jsonl row  -> a log entry at its time ("wrote x: <description>")
//	every memory file           -> a log entry for its current text
//	user, directive memories    -> one rule topic each
//	project memories            -> a wiki page of kind project
//	reference memories          -> a wiki page of kind link
//	any other type              -> a wiki page of kind concept
//
// Each page cites its memory's entry. Rule topics are migrated one per
// memory; dream merges them into topics over time.

// v1Memory is one v1 memory file.
type v1Memory struct {
	Name        string
	Type        string
	Description string
	Body        string
	Modified    time.Time
	Path        string
}

// v1Change is one row of a v1 store's changes.jsonl.
type v1Change struct {
	At     time.Time `json:"at"`
	Op     string    `json:"op"` // write or delete
	Name   string    `json:"name"`
	By     string    `json:"by"`
	Old    string    `json:"old"`
	New    string    `json:"new"`
	Reason string    `json:"reason"`
}

// V1Report says what migrating one store did.
type V1Report struct {
	Store   string
	Entries int
	Rules   int
	Pages   int
	Skip    string   // why the store was passed over: "no v1 memories", "already migrated"
	Errors  []string // files that didn't parse, by path; the rest still migrate
}

// MigrateV1 migrates every store that has v1 memories.
//
// Ensures: v1 files are never written, moved or deleted.
// Ensures: it converges. Entries are appended only to a store whose log has
// no spindle_v1 entry yet; pages are created only where missing, citing the
// spindle_v1 entry whose Path is the memory file. So a rerun after a crash
// finishes the pages, and a rerun after success adds nothing.
func MigrateV1(home string, now time.Time) ([]V1Report, error) {
	panic("unimplemented")
}

// planV1 maps one store's v1 memories and changes to entries and page edits.
// It is the whole mapping, and pure.
//
// Ensures: one entry per change row and one per memory, oldest first; one
// edit per memory, citing that memory's entry; slugs are the v1 names.
func planV1(mems []v1Memory, changes []v1Change, now time.Time) ([]Entry, []Edit) {
	panic("unimplemented")
}
