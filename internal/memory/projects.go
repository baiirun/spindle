package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Projects are named by meaning, not by repository: "spindle", "eldspire",
// "taxes". One may be linked to any number of folders, and a folder hints at
// the project a session is in. Nothing is bound to the hint: search and zoom
// cover every project, and the hint only decides where new entries go and
// which store's rules and timeline a session starts with.

// GlobalName is the store loaded in every project.
const GlobalName = "global"

// UnfiledName is the store for what ingest learns in folders with no
// project. It is never loaded at session start or dreamed; linking a folder
// adopts its entries into the project (see Adopt).
const UnfiledName = "unfiled"

var errNoProject = errors.New("this folder isn't linked to a project: name one (an existing project, or a new name to create it)")

// Meta is a project's project.json.
type Meta struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Folders     []string  `json:"folders,omitempty"`
	Created     time.Time `json:"created"`
}

// Store is the store with this name ("global", "unfiled" or a project),
// which may not exist yet.
func Store(home, name string) Project {
	name = NameOf(name)
	if name == GlobalName || name == UnfiledName {
		return Project{Name: name, Dir: filepath.Join(home, name), Home: home}
	}
	if name == "" {
		return Project{Home: home}
	}
	return Project{Name: name, Dir: filepath.Join(home, "projects", name), Home: home}
}

func (p Project) IsGlobal() bool   { return p.Name == GlobalName }
func (p Project) IsUnfiled() bool  { return p.Name == UnfiledName }
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
	real := resolve(cwd)
	for _, p := range ps {
		for _, f := range p.Meta().Folders {
			if (within(cwd, f) || within(real, resolve(f))) && len(f) > depth {
				best, depth = p, len(f)
			}
		}
	}
	best.Cwd = cwd
	return best, nil
}

// resolve follows symlinks in path, so a folder linked by one spelling
// matches a cwd reported by another (macOS's /var is /private/var). A path
// that can't be resolved is used as is.
func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
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
	Entries int  // log entries
	Rules   int  // rule topics
	Pages   int  // wiki pages
	Context bool // the session's project
}

// ProjectList describes global and every project.
func (p Project) ProjectList() ([]ProjectInfo, error) {
	all, err := stores(p.Home)
	var out []ProjectInfo
	for _, s := range all {
		es, _ := s.Entries()
		rules, _ := s.Rules()
		wiki, _ := s.Wiki()
		out = append(out, ProjectInfo{Meta: s.Meta(), Entries: len(es), Rules: len(rules), Pages: len(wiki), Context: s.Name == p.Name})
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
		fmt.Fprintf(&b, "%s %-16s %4d entries %2d rules %3d pages  %s\n", mark, p.Name, p.Entries, p.Rules, p.Pages, orDash(desc))
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
