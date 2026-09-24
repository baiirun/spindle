package main

import (
	"spindle/internal/episode"
	"spindle/internal/store"
)

// roots are resolved once in main so every command defaults to the same
// durable locations regardless of the caller's working directory.
var roots store.Roots

func loadRoots() error {
	r, err := store.Resolve(episode.PromptVersion)
	if err != nil {
		return err
	}
	roots = r
	return nil
}
