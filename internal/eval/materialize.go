package eval

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/sleep"
)

// Arm is a memory design under test.
type Arm struct {
	Name        string
	Transcripts bool   // raw normalized transcripts before the cutoff
	Notes       string // decision-notes root (e.g. data/memory/decisions-v1); empty = none
}

// Arms are the v0 designs from docs/design.md.
func Arms(notesRoot string) map[string]Arm {
	return map[string]Arm{
		"D0":  {Name: "D0"},
		"D1":  {Name: "D1", Transcripts: true},
		"D2":  {Name: "D2", Transcripts: true, Notes: notesRoot},
		"D2n": {Name: "D2n", Notes: notesRoot},
	}
}

// chunkIndex caches chunk headers so each item's materialization is cheap.
type chunkIndex struct {
	root   string
	chunks []corpus.Chunk
}

func loadChunkIndex(root string) (*chunkIndex, error) {
	paths, err := corpus.ChunkPaths(root)
	if err != nil {
		return nil, err
	}
	idx := &chunkIndex{root: root}
	for _, p := range paths {
		h, err := corpus.ReadChunkHeader(p)
		if err != nil {
			return nil, err
		}
		idx.chunks = append(idx.chunks, h)
	}
	return idx, nil
}

// Materialize builds dir with exactly the memory an arm may use at cutoff:
// whole chunks that ended before it (hard links), truncated copies of chunks
// that span it, and decision notes whose source chunk ended before it.
func Materialize(dir string, arm Arm, idx *chunkIndex, notes []sleep.Note, cutoff time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if arm.Transcripts {
		for _, c := range idx.chunks {
			if !c.Start.Before(cutoff) {
				continue
			}
			rel, err := filepath.Rel(idx.root, c.Path)
			if err != nil {
				return err
			}
			dst := filepath.Join(dir, "transcripts", rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if c.End.Before(cutoff) {
				if err := os.Link(c.Path, dst); err != nil {
					return err
				}
				continue
			}
			if err := writeTruncated(c.Path, dst, cutoff); err != nil {
				return err
			}
		}
	}
	if arm.Notes != "" {
		for _, n := range notes {
			if !n.AvailableAt.Before(cutoff) {
				continue
			}
			dst := filepath.Join(dir, "decisions", n.Path)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.Link(filepath.Join(arm.Notes, "notes", n.Path), dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeTruncated(src, dst string, cutoff time.Time) error {
	c, err := corpus.ReadChunk(src)
	if err != nil {
		return err
	}
	var kept []corpus.Item
	for _, it := range c.Items {
		if it.Time.Before(cutoff) {
			kept = append(kept, it)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	c.Items = kept
	c.End = kept[len(kept)-1].Time
	return os.WriteFile(dst, []byte(c.Render()), 0o644)
}

// citationPattern matches the IDs a reader may cite.
const citationPattern = `(?:codex|claude):[0-9a-f-]+#L\d+|d-[0-9a-f]{8}`

// citationsValid checks each cited ID against the materialized memory, so a
// citation to something the arm couldn't see counts as invalid.
func citationsValid(dir string, ids []string) (valid int, err error) {
	var files []string
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	remaining := map[string]bool{}
	for _, id := range ids {
		remaining[id] = true
	}
	for _, f := range files {
		if len(remaining) == 0 {
			break
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return 0, err
		}
		s := string(b)
		for id := range remaining {
			if strings.Contains(s, id) {
				delete(remaining, id)
				valid++
			}
		}
	}
	return valid, nil
}
