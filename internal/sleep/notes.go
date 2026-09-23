package sleep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Note is a decision note on disk plus the time it became available.
type Note struct {
	ID          string    `json:"id"`
	Path        string    `json:"path"` // relative to the notes root
	AvailableAt time.Time `json:"available_at"`
}

// WriteNotes regenerates the Markdown notes and manifest from the chunk cache.
// Notes are a pure function of the cache, so this is safe to rerun.
func WriteNotes(outRoot string) error {
	notesRoot := filepath.Join(outRoot, "notes")
	if err := os.RemoveAll(notesRoot); err != nil {
		return err
	}
	var manifest []Note
	err := filepath.WalkDir(filepath.Join(outRoot, "cache"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var r ChunkResult
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		for i, d := range r.Decisions {
			sum := sha256.Sum256([]byte(r.Chunk + "\x00" + d.Note))
			id := "d-" + hex.EncodeToString(sum[:])[:8]
			rel := filepath.Join(r.Project, id+".md")
			full := filepath.Join(notesRoot, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			content := fmt.Sprintf("---\nid: %s\ntime: %s\nproject: %s\nevidence: [%s]\n---\n%s\n",
				id, r.Times[i], r.Project, strings.Join(d.Evidence, ", "), d.Note)
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return err
			}
			manifest = append(manifest, Note{ID: id, Path: rel, AvailableAt: r.AvailableAt})
		}
		return nil
	})
	if err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(outRoot, "manifest.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, n := range manifest {
		if err := enc.Encode(n); err != nil {
			return err
		}
	}
	return nil
}

// ReadManifest loads the note manifest for a design.
func ReadManifest(outRoot string) ([]Note, error) {
	b, err := os.ReadFile(filepath.Join(outRoot, "manifest.jsonl"))
	if err != nil {
		return nil, err
	}
	var notes []Note
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var n Note
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, nil
}
