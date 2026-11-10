// Package tui is spin tui, a read-only viewer: stores on the left, a store's
// layers (rules, wiki, timeline, log) in the middle, the selected item's
// text, cited entries or change history on the right.
package tui

import (
	"spindle/internal/memory"
)

// The viewer reads the stores directly rather than through memory.Client:
// every Client call goes to the activity log, and browsing would fill it with
// reads nobody made in a session.

// Snapshot is everything the viewer shows, read from the stores at once.
type Snapshot struct {
	Stores []Store        // global first, then the projects by name
	Events []memory.Event // the activity log, oldest first
}

// Store is one store's layers.
type Store struct {
	Name        string
	Description string
	Context     bool // the folder's project
	Rules       []memory.Rule
	Wiki        []memory.WikiPage
	Timeline    []memory.Node
	Entries     []memory.Entry
	Changes     []memory.Change
}

// Load reads global, every project and the activity log. context names the
// project to mark as the folder's ("" for none).
func Load(home, context string) (Snapshot, error) {
	panic("unimplemented")
}

// Run opens the viewer on store start.
func Run(home, start, context string) error {
	panic("unimplemented")
}
