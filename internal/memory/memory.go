// Package memory is long-term memory for all of a user's projects, kept in
// four layers per store:
//
//   - the log: one entry per thing worth remembering, a one-line gist and a
//     pointer to where it was said. It is the source of truth and only grows.
//   - rollups: one-line summaries per session, day and week, written by a
//     side model. They are a cache: deleting one costs a model call, never a fact.
//   - rules: directives an agent loads before acting, a few topic files.
//   - the wiki: one page per entity (person, project, link, tool, concept,
//     org), each citing the log entries it rests on.
//
// Ingest appends to the log, rollups summarize it, and dream reads what is
// new and maintains rules and the wiki. People edit rules and pages too, so
// pages are authoritative in their own right: dream edits them and never
// regenerates them from the log. Every page change is recorded with its
// reason and evidence.
//
// Layout under <SPINDLE_HOME>:
//
//	global/                    the store loaded in every project
//	unfiled/                   entries from folders with no project, until one is linked
//	projects/<name>/           one project's store:
//	  project.json             name, description, linked folders
//	  log.jsonl                the log
//	  rollups/<kind>/<key>.json one rollup per window (s, d or w)
//	  rules/<slug>.md          one rule topic
//	  wiki/<slug>.md           one entity page
//	  page-changes.jsonl       every rule and wiki change, with before and after
//	  dream.json               how far dream has read the log
//	  dream-session.json(l)    a running dream's session and its commands' outcomes
//	  lock                     flock held by every writer of the store
//	  memory/, changes.jsonl   spindle v1, read by MigrateV1 and never written
//	activity.jsonl             what spindle did, for people reading along
//	cursors.json               how far ingest has read each session
//	sources.json, sweeps.json  where sessions come from, how far each is swept
//	queue/                     ingest jobs
//
// Invariants:
//   - Only Append writes log.jsonl, and it never rewrites a line.
//   - Rollups, the session-start index and search results can be rebuilt
//     from the log and the pages; removing them changes no answer.
//   - Every rule and wiki page write goes through ApplyEdits, which records it.
//   - Subagents and side-model processes (SPINDLE_INNER) never write memory.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// HomeEnv selects the data root; ProjectEnv names the session's project.
const (
	HomeEnv    = "SPINDLE_HOME"
	ProjectEnv = "SPINDLE_PROJECT"
	// InnerEnv is set on every process spindle starts. Hooks that see it do
	// nothing, so a side-model call through claude or codex can't recurse.
	InnerEnv = "SPINDLE_INNER"
)

// Home returns SPINDLE_HOME or ~/.spindle.
func Home() (string, error) {
	if h := strings.TrimSpace(os.Getenv(HomeEnv)); h != "" {
		if !filepath.IsAbs(h) {
			return "", fmt.Errorf("%s must be absolute", HomeEnv)
		}
		return filepath.Clean(h), nil
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(u, ".spindle"), nil
}

// Project is one store: a named project, or global. As the receiver of the
// cross-store methods (Index, Ingest routing) it is also the session's
// context: the project its folder hints at, which may be none (Name ""), and
// that folder.
type Project struct {
	Name string
	Dir  string
	Home string
	Cwd  string // the session's folder, when known
}

// Init creates the store's folder and its project.json.
func (p Project) Init() error {
	if p.Name == "" {
		return errNoProject
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(p.metaPath()); err == nil {
		return nil
	}
	return writeJSON(p.metaPath(), Meta{Name: p.Name, Created: time.Now().UTC()})
}

// Lock takes the store's writer lock.
func (p Project) Lock() (func(), error) {
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return nil, err
	}
	return lockFile(filepath.Join(p.Dir, "lock"), true)
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// NameOf normalizes a project name to a kebab-case slug.
func NameOf(s string) string {
	n := strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if len(n) > 64 {
		n = strings.TrimRight(n[:64], "-")
	}
	return n
}
