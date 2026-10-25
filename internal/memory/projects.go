package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Projects are named by meaning, not by repository: "spindle", "eldspire",
// "taxes". One may be linked to any number of folders, and a folder hints at
// the project a session is in. Nothing is bound to the hint: recall and search
// cover every project, and the hint only decides where a new memory goes by
// default and which index a session starts with.

// GlobalName is the store loaded in every project.
const GlobalName = "global"

var errNoProject = errors.New("this folder isn't linked to a project: name one (an existing project, or a new name to create it)")

// Meta is a project's project.json.
type Meta struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Folders     []string  `json:"folders,omitempty"`
	Created     time.Time `json:"created"`
}

// Store is the store with this name ("global" or a project), which may not
// exist yet.
func Store(home, name string) Project {
	name = NameOf(name)
	if name == GlobalName {
		return Project{Name: name, Dir: filepath.Join(home, GlobalName), Home: home}
	}
	if name == "" {
		return Project{Home: home}
	}
	return Project{Name: name, Dir: filepath.Join(home, "projects", name), Home: home}
}

func (p Project) IsGlobal() bool   { return p.Name == GlobalName }
func (p Project) metaPath() string { return filepath.Join(p.Dir, "project.json") }

// Exists reports whether the store has been created.
func (p Project) Exists() bool {
	if p.Name == "" {
		return false
	}
	_, err := os.Stat(p.Dir)
	return err == nil
}

// Meta reads project.json.
func (p Project) Meta() Meta {
	m := Meta{Name: p.Name}
	if p.Name != "" {
		_ = readJSON(p.metaPath(), &m)
	}
	m.Name = p.Name
	return m
}

// UpdateMeta changes project.json under the store's lock.
func (p Project) UpdateMeta(f func(*Meta)) error {
	if err := p.Init(); err != nil {
		return err
	}
	unlock, err := p.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	m := p.Meta()
	f(&m)
	return writeJSON(p.metaPath(), m)
}

// Link makes dir hint at this project.
func (p Project) Link(dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return p.UpdateMeta(func(m *Meta) {
		if !contains(m.Folders, dir) {
			m.Folders = append(m.Folders, dir)
		}
	})
}

// Unlink removes dir from the project's folders.
func (p Project) Unlink(dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return p.UpdateMeta(func(m *Meta) {
		var keep []string
		for _, f := range m.Folders {
			if f != dir {
				keep = append(keep, f)
			}
		}
		m.Folders = keep
	})
}

// Projects lists the named projects (not global), by name.
func Projects(home string) ([]Project, error) {
	if err := migrate(home); err != nil {
		return nil, err
	}
	dirs, err := filepath.Glob(filepath.Join(home, "projects", "*", "project.json"))
	if err != nil {
		return nil, err
	}
	var out []Project
	for _, d := range dirs {
		if p := Store(home, filepath.Base(filepath.Dir(d))); p.Name != "" && !p.IsGlobal() {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// stores is global then every project.
func stores(home string) ([]Project, error) {
	ps, err := Projects(home)
	return append([]Project{Store(home, GlobalName)}, ps...), err
}

// Hint is the session context for a folder: SPINDLE_PROJECT if set (a
// project name), else the project linked to the deepest folder containing
// cwd, else no project.
func Hint(home, cwd string) (Project, error) {
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return Project{}, err
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return Project{}, err
	}
	if name := strings.TrimSpace(os.Getenv(ProjectEnv)); name != "" {
		p := Store(home, name)
		p.Cwd = cwd
		return p, nil
	}
	ps, err := Projects(home)
	if err != nil {
		return Project{}, err
	}
	best, depth := Project{Home: home}, -1
	for _, p := range ps {
		for _, f := range p.Meta().Folders {
			if within(cwd, f) && len(f) > depth {
				best, depth = p, len(f)
			}
		}
	}
	best.Cwd = cwd
	return best, nil
}

func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gitRoot is the nearest directory at or above dir with a .git entry, or dir.
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

// ProjectInfo is one line of the project list.
type ProjectInfo struct {
	Meta
	Memories int
	Context  bool // the session's project
}

// ProjectList describes global and every project.
func (p Project) ProjectList() ([]ProjectInfo, error) {
	all, err := stores(p.Home)
	var out []ProjectInfo
	for _, s := range all {
		mems, _ := s.Load()
		out = append(out, ProjectInfo{Meta: s.Meta(), Memories: len(mems), Context: s.Name == p.Name})
	}
	return out, err
}

func FormatProjects(ps []ProjectInfo) string {
	var b strings.Builder
	for _, p := range ps {
		mark := " "
		if p.Context {
			mark = "*"
		}
		desc := p.Description
		if p.Name == GlobalName {
			desc = "loaded in every project"
		}
		fmt.Fprintf(&b, "%s %-16s %3d  %s\n", mark, p.Name, p.Memories, orDash(desc))
		for _, f := range p.Folders {
			fmt.Fprintf(&b, "  %-16s      %s\n", "", f)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// --- migration from stores keyed by repository root ---

var oldSlug = regexp.MustCompile(`^(.+)-[0-9a-f]{8}$`)

// migrate renames the stores of earlier versions, projects/<base>-<hash>
// with project.json {"root": ...}, to projects/<base> linked to that root,
// and moves their per-store logs and cursors to the home.
func migrate(home string) error {
	dirs, _ := filepath.Glob(filepath.Join(home, "projects", "*-*"))
	var old []string
	for _, d := range dirs {
		var meta struct{ Root, Name string }
		if oldSlug.MatchString(filepath.Base(d)) && readJSON(filepath.Join(d, "project.json"), &meta) == nil && meta.Root != "" && meta.Name == "" {
			old = append(old, d)
		}
	}
	if len(old) == 0 {
		return nil
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(home, "migrate.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	for _, d := range old {
		if err := migrateOne(home, d); err != nil {
			return fmt.Errorf("migrate %s: %w", d, err)
		}
	}
	return nil
}

func migrateOne(home, dir string) error {
	var meta struct {
		Root    string    `json:"root"`
		Name    string    `json:"name"`
		Created time.Time `json:"created"`
	}
	if err := readJSON(filepath.Join(dir, "project.json"), &meta); err != nil || meta.Root == "" || meta.Name != "" {
		return err // another process got here first
	}
	base := NameOf(oldSlug.FindStringSubmatch(filepath.Base(dir))[1])
	if base == "" || base == GlobalName {
		base = "project"
	}
	name := base
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(home, "projects", name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	// Cursors move to the home; the dream gate stays with the store.
	var st struct {
		Cursors map[string]*Cursor `json:"cursors"`
	}
	_ = readJSON(filepath.Join(dir, "state.json"), &st)
	if len(st.Cursors) > 0 {
		if err := updateCursors(home, func(c map[string]*Cursor) {
			for path, cur := range st.Cursors {
				if have := c[path]; have == nil || have.Line < cur.Line {
					c[path] = cur
				}
			}
		}); err != nil {
			return err
		}
	}
	if evs, _ := readJSONL[Event](filepath.Join(dir, "events.jsonl")); len(evs) > 0 {
		have, _ := readJSONL[Event](filepath.Join(home, "events.jsonl"))
		for i := range evs {
			evs[i].Project = name
		}
		all := append(evs, have...)
		sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
		tmp := filepath.Join(home, "events.jsonl.migrate")
		os.Remove(tmp)
		if err := appendJSONL(tmp, all); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(home, "events.jsonl")); err != nil {
			return err
		}
		os.Remove(filepath.Join(dir, "events.jsonl"))
	}
	os.RemoveAll(filepath.Join(dir, "recall"))
	if err := writeJSON(filepath.Join(dir, "project.json"), Meta{Name: name, Folders: []string{meta.Root}, Created: meta.Created}); err != nil {
		return err
	}
	return os.Rename(dir, filepath.Join(home, "projects", name))
}
