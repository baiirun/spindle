// Package snapshot builds a view of spindle data as it existed at a moment in
// time. Continuation trials point SPINDLE_HOME at a snapshot so every read
// verb (search, wake, read, resume, related) is leak-free by construction,
// instead of each verb enforcing its own time filter.
package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/episode"
)

// Options configures one snapshot.
type Options struct {
	CorpusRoot  string // source corpus root
	EpisodeRoot string // source episode root for one episode version
	Out         string // snapshot home; receives corpus/ and episodes/<version>/
	Version     string // episode version directory name
	Before      time.Time
}

// Stats counts what the snapshot kept.
type Stats struct {
	Chunks, Truncated, Episodes int
}

// Build writes the snapshot. Out must not already exist, so a snapshot is
// never mixed with later data.
//
// Rules: a corpus chunk is kept whole (hard link) if it ended before the
// cutoff, truncated to items before the cutoff if it spans it, and dropped if
// it starts at or after it. An episode is kept only if its source range ended
// before the cutoff; an episode over a chunk that spans the cutoff summarizes
// content the trial must not see.
func Build(o Options) (Stats, error) {
	var st Stats
	if _, err := os.Stat(o.Out); err == nil {
		return st, fmt.Errorf("snapshot %s already exists", o.Out)
	}
	corpusOut := filepath.Join(o.Out, "corpus")
	episodesOut := filepath.Join(o.Out, "episodes", o.Version)
	if err := os.MkdirAll(corpusOut, 0o755); err != nil {
		return st, err
	}
	if err := os.MkdirAll(episodesOut, 0o755); err != nil {
		return st, err
	}

	paths, err := corpus.ChunkPaths(o.CorpusRoot)
	if err != nil {
		return st, err
	}
	for _, p := range paths {
		h, err := corpus.ReadChunkHeader(p)
		if err != nil {
			return st, err
		}
		if !h.Start.Before(o.Before) {
			continue
		}
		dst := filepath.Join(corpusOut, h.Source, h.Session, filepath.Base(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return st, err
		}
		if h.End.Before(o.Before) {
			if err := os.Link(p, dst); err != nil {
				return st, err
			}
			st.Chunks++
			continue
		}
		kept, err := writeTruncated(p, dst, o.Before)
		if err != nil {
			return st, err
		}
		if kept {
			st.Chunks++
			st.Truncated++
		}
	}

	if o.EpisodeRoot == "" {
		return st, nil
	}
	err = filepath.WalkDir(o.EpisodeRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		e, err := episode.ReadPath(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if e.End.IsZero() || !e.End.Before(o.Before) {
			return nil
		}
		rel, err := filepath.Rel(o.EpisodeRoot, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(episodesOut, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		st.Episodes++
		return os.Link(p, dst)
	})
	return st, err
}

func writeTruncated(src, dst string, before time.Time) (bool, error) {
	c, err := corpus.ReadChunk(src)
	if err != nil {
		return false, err
	}
	var kept []corpus.Item
	for _, it := range c.Items {
		if it.Time.Before(before) {
			kept = append(kept, it)
		}
	}
	if len(kept) == 0 {
		return false, nil
	}
	c.Items = kept
	c.End = kept[len(kept)-1].Time
	return true, os.WriteFile(dst, []byte(c.Render()), 0o644)
}
