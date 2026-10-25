// Package source finds the sessions spindle learns from. A source is one
// place sessions live: Claude Code's transcripts, Codex's, and later others
// such as cloud sessions. Every source does the same two jobs: list the
// sessions that changed in a time window, and read one session's messages.
// Which sources spindle reads is set in <SPINDLE_HOME>/sources.json.
package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"spindle/internal/transcript"
)

// Ref names one session a source found.
type Ref struct {
	Source  string    // the source's kind
	Agent   string    // the tool that wrote it, claude or codex; it picks the side models
	Loc     string    // where the source reads it again: for a local source, the transcript's path
	Updated time.Time // when it last changed
}

// Source is one place sessions live.
type Source interface {
	Kind() string
	// List returns the sessions that changed after since and up to until.
	List(ctx context.Context, since, until time.Time) ([]Ref, error)
	// Read returns a session's messages. Each message's Line is its
	// position in the session and only grows, which is what cursors count.
	Read(ctx context.Context, loc string) (transcript.Session, error)
}

// Spec is one entry of sources.json.
type Spec struct {
	Kind string `json:"kind"`          // claude-code or codex
	Dir  string `json:"dir,omitempty"` // where its transcripts live, if not the tool's default
}

// Config is sources.json.
type Config struct {
	Sources []Spec `json:"sources"`
}

// Defaults are read when sources.json doesn't exist: the two local tools.
var Defaults = Config{Sources: []Spec{{Kind: ClaudeCode}, {Kind: Codex}}}

const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// ConfigPath is where the sources are listed.
func ConfigPath(home string) string { return filepath.Join(home, "sources.json") }

// LoadConfig reads sources.json, or the defaults when it doesn't exist.
func LoadConfig(home string) (Config, error) {
	b, err := os.ReadFile(ConfigPath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ConfigPath(home), err)
	}
	return c, nil
}

// New builds the source a spec describes.
func New(s Spec) (Source, error) {
	switch s.Kind {
	case ClaudeCode:
		dir := s.Dir
		if dir == "" {
			dir = claudeDir()
		}
		return Files{kind: ClaudeCode, agent: "claude", dir: expand(dir), skipDir: "subagents"}, nil
	case Codex:
		dir := s.Dir
		if dir == "" {
			dir = codexDir()
		}
		return Files{kind: Codex, agent: "codex", dir: expand(dir)}, nil
	}
	return nil, fmt.Errorf("unknown source kind %q", s.Kind)
}

// Load builds every configured source. A spec that can't be built is
// returned as an error beside the sources that can.
func Load(home string) ([]Source, error) {
	c, err := LoadConfig(home)
	if err != nil {
		return nil, err
	}
	var out []Source
	var errs []error
	for _, s := range c.Sources {
		src, err := New(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, src)
	}
	return out, errors.Join(errs...)
}

// Read reads a session by where a source keeps it. Every source so far keeps
// sessions in local transcript files, so a location is a path.
func Read(ctx context.Context, loc string) (transcript.Session, error) {
	if !filepath.IsAbs(loc) {
		return transcript.Session{}, fmt.Errorf("no source reads %q", loc)
	}
	return transcript.Read(loc)
}

// Files is a source whose sessions are JSONL transcripts under one folder.
type Files struct {
	kind, agent string
	dir         string
	skipDir     string // folders of this name hold subagents' transcripts
}

func (f Files) Kind() string { return f.kind }

// Dir is the folder the source reads.
func (f Files) Dir() string { return f.dir }

func (f Files) List(ctx context.Context, since, until time.Time) ([]Ref, error) {
	var out []Ref
	err := filepath.WalkDir(f.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == f.dir && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll // the tool isn't installed
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if f.skipDir != "" && d.Name() == f.skipDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if mt := info.ModTime(); mt.After(since) && !mt.After(until) {
			out = append(out, Ref{Source: f.kind, Agent: f.agent, Loc: path, Updated: mt})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.Before(out[j].Updated) })
	return out, err
}

func (f Files) Read(ctx context.Context, loc string) (transcript.Session, error) {
	return transcript.Read(loc)
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	return "~/.claude/projects"
}

func codexDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "sessions")
	}
	return "~/.codex/sessions"
}

func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}
