// Package memory is spindle's live project memory: what the hooks inject into
// Claude Code and Codex sessions and what the MCP tools read and write. See
// docs/hooks.md.
//
// One project is one memory bucket (docs/variants.md: one long-running
// coordinator per project). Its files live under <SPINDLE_HOME>/projects/<slug>:
//
//	project.json   the project root this bucket belongs to
//	now.md         the "Now" half of the continuation summary, rewritten fresh at each extraction
//	record.json    the decision record: decisions, tried or rejected, preferences; edited by add/replace/drop
//	users.jsonl    every user message, verbatim, in the order extraction saw them
//	state.json     per-transcript cursors, dream gate, per-session recall state
//	lock           flock held by every writer
//
// Jobs from the Stop hook wait in <SPINDLE_HOME>/queue until the worker drains them.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ProjectEnv pins the project root, overriding detection from the working directory.
const ProjectEnv = "SPINDLE_PROJECT"

// InnerEnv is set on every process the worker starts. Hooks see it and do
// nothing, so a model call that runs Codex can't fire spindle's own hooks again.
const InnerEnv = "SPINDLE_INNER"

// Project is one memory bucket.
type Project struct {
	Root string // absolute project directory, usually the git top level
	Slug string
	Dir  string // <home>/projects/<slug>
	Home string // SPINDLE_HOME
}

// ResolveProject maps a working directory to its project: SPINDLE_PROJECT if
// set, else the nearest enclosing directory with a .git entry, else cwd itself.
func ResolveProject(home, cwd string) (Project, error) {
	root := strings.TrimSpace(os.Getenv(ProjectEnv))
	if root == "" {
		if cwd == "" {
			var err error
			if cwd, err = os.Getwd(); err != nil {
				return Project{}, err
			}
		}
		root = gitRoot(cwd)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Project{}, err
	}
	return ProjectAt(home, abs), nil
}

// ProjectAt is the project for an already resolved root.
func ProjectAt(home, root string) Project {
	root = filepath.Clean(root)
	slug := Slug(root)
	return Project{Root: root, Slug: slug, Dir: filepath.Join(home, "projects", slug), Home: home}
}

func gitRoot(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}

var unsafeSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug is the readable, collision-safe directory name for a project root:
// the base name plus a short hash of the full path.
func Slug(root string) string {
	base := unsafeSlug.ReplaceAllString(strings.ToLower(filepath.Base(root)), "-")
	base = strings.Trim(base, "-.")
	if base == "" {
		base = "root"
	}
	sum := sha256.Sum256([]byte(root))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// Name is the short name used in injected context.
func (p Project) Name() string { return filepath.Base(p.Root) }

func (p Project) path(name string) string { return filepath.Join(p.Dir, name) }

// Init creates the project directory and records its root.
func (p Project) Init() error {
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(p.path("project.json")); err == nil {
		return nil
	}
	return writeJSON(p.path("project.json"), map[string]any{"root": p.Root, "created": time.Now().UTC()})
}

// Lock takes the project's exclusive writer lock. Readers never lock: every
// file is replaced atomically, so they see either the old or the new version.
func (p Project) Lock() (unlock func(), err error) {
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return nil, err
	}
	return lockFile(p.path("lock"), true)
}

func (p Project) String() string { return fmt.Sprintf("%s (%s)", p.Name(), p.Root) }
