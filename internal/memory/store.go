// Package memory is a project's long-term memory, after Claude Code's auto
// memory: a folder of Markdown files, one subject each, typed and described in
// one line, plus a MEMORY.md index that is always loaded. Files are rewritten
// in place when a fact changes and deleted when it turns out wrong; every
// write and delete, with the old text, also goes to changes.jsonl so nothing
// is lost for audit.
//
// Layout under <SPINDLE_HOME>/projects/<name>-<hash>/:
//
//	memory/MEMORY.md     index, rebuilt by code after every change
//	memory/<name>.md     one memory
//	changes.jsonl        audit log of writes and deletes
//	state.json           transcript cursors, sessions seen, dream gate
//	recall/<session>.json what one session was shown, and its pending selection
//	lock                 flock held by every writer
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Types follow Claude Code's taxonomy, with "feedback" called "directive".
var Types = []string{"user", "directive", "project", "reference"}

func ValidType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}

// Limits, from Claude Code: the index loads its first 200 lines or 25 KB, and
// a recalled file is attached up to 4 KB.
const (
	IndexLines = 200
	IndexBytes = 25_000
	FileBytes  = 4_096
)

// HomeEnv selects the data root; ProjectEnv pins the project root.
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

// Project is one memory bucket: everything under one project root.
type Project struct {
	Root string
	Dir  string
	Home string
}

// Resolve maps a working directory to its project: SPINDLE_PROJECT, else the
// nearest directory above with a .git entry, else the directory itself.
func Resolve(home, cwd string) (Project, error) {
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
	return At(home, abs), nil
}

// At is the project for a known root.
func At(home, root string) Project {
	root = filepath.Clean(root)
	return Project{Root: root, Dir: filepath.Join(home, "projects", slug(root)), Home: home}
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

var slugUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

func slug(root string) string {
	base := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(filepath.Base(root)), "-"), "-.")
	if base == "" {
		base = "root"
	}
	sum := sha256.Sum256([]byte(root))
	return base + "-" + hex.EncodeToString(sum[:4])
}

func (p Project) Name() string      { return filepath.Base(p.Root) }
func (p Project) MemDir() string    { return filepath.Join(p.Dir, "memory") }
func (p Project) IndexPath() string { return filepath.Join(p.MemDir(), "MEMORY.md") }

// Init creates the folders and records the root.
func (p Project) Init() error {
	if err := os.MkdirAll(p.MemDir(), 0o755); err != nil {
		return err
	}
	meta := filepath.Join(p.Dir, "project.json")
	if _, err := os.Stat(meta); err == nil {
		return nil
	}
	return writeJSON(meta, map[string]any{"root": p.Root, "created": time.Now().UTC()})
}

// Memory is one file.
type Memory struct {
	Name        string
	Description string
	Type        string
	Body        string
	Modified    time.Time // file mtime
}

// File is the memory's file name.
func (m Memory) File() string { return m.Name + ".md" }

// Render writes the file in Claude Code's format.
func (m Memory) Render() string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\nmetadata:\n  type: %s\n---\n\n%s\n", m.Name, oneLine(m.Description), m.Type, strings.TrimSpace(m.Body))
}

// Parse reads a memory file. It tolerates hand edits: a flat "type:" key
// works too, and unknown keys are ignored.
func Parse(text string) (Memory, error) {
	var m Memory
	if !strings.HasPrefix(text, "---\n") {
		return m, errors.New("no frontmatter")
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return m, errors.New("unterminated frontmatter")
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "name":
			m.Name = v
		case "description":
			m.Description = v
		case "type":
			m.Type = v
		}
	}
	rest := text[4+end+4:]
	m.Body = strings.TrimSpace(rest)
	return m, nil
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// NameOf normalizes a memory name to a kebab-case slug, which is also its file stem.
func NameOf(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".md")
	n := strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(n) > 64 {
		n = strings.TrimRight(n[:64], "-")
	}
	return n
}

// Load reads every memory, newest modified first.
func (p Project) Load() ([]Memory, error) {
	paths, err := filepath.Glob(filepath.Join(p.MemDir(), "*.md"))
	if err != nil {
		return nil, err
	}
	var out []Memory
	for _, path := range paths {
		if filepath.Base(path) == "MEMORY.md" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		m, err := Parse(string(b))
		if err != nil {
			continue // a broken hand edit must not take the whole memory down
		}
		m.Name = strings.TrimSuffix(filepath.Base(path), ".md")
		if fi, err := os.Stat(path); err == nil {
			m.Modified = fi.ModTime()
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Modified.Equal(out[j].Modified) {
			return out[i].Modified.After(out[j].Modified)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Get reads one memory.
func (p Project) Get(name string) (Memory, bool) {
	name = NameOf(name)
	b, err := os.ReadFile(filepath.Join(p.MemDir(), name+".md"))
	if err != nil {
		return Memory{}, false
	}
	m, err := Parse(string(b))
	if err != nil {
		return Memory{}, false
	}
	m.Name = name
	if fi, err := os.Stat(filepath.Join(p.MemDir(), name+".md")); err == nil {
		m.Modified = fi.ModTime()
	}
	return m, true
}

// Op is one change to memory.
type Op struct {
	Op          string `json:"op"` // write | delete
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Reason      string `json:"reason"` // for delete
}

// Change is one audit-log line.
type Change struct {
	At      time.Time `json:"at"`
	Op      string    `json:"op"`
	Name    string    `json:"name"`
	By      string    `json:"by"` // agent | extract | dream
	Session string    `json:"session,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Old     string    `json:"old,omitempty"` // the file before the change
	New     string    `json:"new,omitempty"`
}

// Result counts what Apply did.
type Result struct {
	Written, Deleted, Skipped int
	Changed                   []string // "+name" written, "-name" deleted
}

// Apply writes and deletes memories, logs each change, and rebuilds the
// index. Callers hold the project lock.
func (p Project) Apply(ops []Op, by, session string, now time.Time) (Result, error) {
	var res Result
	if err := p.Init(); err != nil {
		return res, err
	}
	var log []Change
	for _, o := range ops {
		name := NameOf(o.Name)
		if name == "" || name == "memory" {
			res.Skipped++
			continue
		}
		path := filepath.Join(p.MemDir(), name+".md")
		oldBytes, _ := os.ReadFile(path)
		old := string(oldBytes)
		switch o.Op {
		case "write":
			typ := strings.TrimSpace(o.Type)
			if typ == "" && old != "" {
				prev, _ := Parse(old)
				typ = prev.Type
			}
			desc := oneLine(o.Description)
			if desc == "" && old != "" {
				prev, _ := Parse(old)
				desc = prev.Description
			}
			if !ValidType(typ) || strings.TrimSpace(o.Body) == "" || desc == "" {
				res.Skipped++
				continue
			}
			text := Memory{Name: name, Description: desc, Type: typ, Body: o.Body}.Render()
			if text == old {
				continue
			}
			if err := writeFile(path, []byte(text)); err != nil {
				return res, err
			}
			res.Written++
			res.Changed = append(res.Changed, "+"+name)
			log = append(log, Change{At: now.UTC(), Op: "write", Name: name, By: by, Session: session, Old: old, New: text})
		case "delete":
			if old == "" {
				res.Skipped++
				continue
			}
			if err := os.Remove(path); err != nil {
				return res, err
			}
			res.Deleted++
			res.Changed = append(res.Changed, "-"+name)
			log = append(log, Change{At: now.UTC(), Op: "delete", Name: name, By: by, Session: session, Reason: oneLine(o.Reason), Old: old})
		default:
			res.Skipped++
		}
	}
	if len(log) == 0 {
		return res, nil
	}
	if err := appendJSONL(filepath.Join(p.Dir, "changes.jsonl"), log); err != nil {
		return res, err
	}
	return res, p.RebuildIndex()
}

// RebuildIndex writes MEMORY.md: one line per memory, grouped by type, in
// Claude Code's "- [Title](file.md) — hook" form, within its load limits.
func (p Project) RebuildIndex() error {
	mems, err := p.Load()
	if err != nil {
		return err
	}
	return writeFile(p.IndexPath(), []byte(Index(mems)))
}

var typeTitles = map[string]string{"user": "User", "directive": "Directives", "project": "Project", "reference": "References"}

// Index renders the index text. Within a type, newest first, so when a big
// project overflows the limits it is the oldest lines that fall off.
func Index(mems []Memory) string {
	var b strings.Builder
	lines, dropped := 0, 0
	for _, t := range Types {
		var group []Memory
		for _, m := range mems {
			if m.Type == t {
				group = append(group, m)
			}
		}
		if len(group) == 0 {
			continue
		}
		head := fmt.Sprintf("## %s\n", typeTitles[t])
		if lines > 0 {
			head = "\n" + head
		}
		if lines+2 > IndexLines || b.Len()+len(head) > IndexBytes {
			dropped += len(group)
			continue
		}
		b.WriteString(head)
		lines += 2
		for _, m := range group {
			line := fmt.Sprintf("- [%s](%s) — %s\n", m.Name, m.File(), clipRunes(oneLine(m.Description), 140))
			if lines+1 > IndexLines-1 || b.Len()+len(line) > IndexBytes-200 {
				dropped++
				continue
			}
			b.WriteString(line)
			lines++
		}
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "\n(%d older memories not listed here; search for them.)\n", dropped)
	}
	return b.String()
}

// ReadIndex returns MEMORY.md, or "" if there is none.
func (p Project) ReadIndex() string {
	b, _ := os.ReadFile(p.IndexPath())
	return string(b)
}

// Lock takes the project's writer lock.
func (p Project) Lock() (func(), error) {
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return nil, err
	}
	return lockFile(filepath.Join(p.Dir, "lock"), true)
}

// --- small file helpers ---

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'))
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func appendJSONL[T any](path string, rows []T) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			f.Close()
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

var errLocked = errors.New("locked")

// lockFile takes an flock; the kernel releases it if the process dies.
func lockFile(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func readJSONL[T any](path string) ([]T, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []T
	for _, line := range strings.Split(string(b), "\n") {
		var v T
		if strings.TrimSpace(line) != "" && json.Unmarshal([]byte(line), &v) == nil {
			out = append(out, v)
		}
	}
	return out, nil
}
