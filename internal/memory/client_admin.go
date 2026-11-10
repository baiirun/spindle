package memory

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spindle/internal/source"
)

// The Client's project, source and activity calls: unchanged from v1.

// Projects lists global and every project.
func (c Client) Projects() ([]ProjectInfo, error) {
	p := c.Context()
	c.log(p, "projects", nil, "", "", time.Now(), nil)
	return p.ProjectList()
}

// SetProject creates or describes a project and links or unlinks folders.
// link "." means the session's folder (its repository root).
func (c Client) SetProject(name, description, link, unlink string) (string, error) {
	start := time.Now()
	ctx := c.Context()
	s := Store(c.Home, name)
	msg, err := func() (string, error) {
		if s.Name == "" || s.IsGlobal() || s.IsUnfiled() {
			return "", fmt.Errorf("%q can't be a project name", name)
		}
		existed := s.Exists()
		if err := s.UpdateMeta(func(m *Meta) {
			if d := oneLine(description); d != "" {
				m.Description = d
			}
		}); err != nil {
			return "", err
		}
		var notes []string
		if !existed {
			notes = append(notes, "Created project "+s.Name+".")
		} else if description != "" {
			notes = append(notes, "Described "+s.Name+".")
		}
		if link == "." {
			link = gitRoot(c.Cwd)
		}
		if link != "" {
			if err := s.Link(link); err != nil {
				return "", err
			}
			notes = append(notes, "Linked "+link+".")
			n, err := s.Adopt(link, time.Now())
			if err != nil {
				return "", err
			}
			if n > 0 {
				notes = append(notes, fmt.Sprintf("Adopted %d unfiled entries from it.", n))
			}
		}
		if unlink != "" {
			if err := s.Unlink(unlink); err != nil {
				return "", err
			}
			notes = append(notes, "Unlinked "+unlink+".")
		}
		if len(notes) == 0 {
			notes = append(notes, "Unchanged.")
		}
		return strings.Join(notes, " "), nil
	}()
	c.log(ctx, "project", nil, "", s.Name, start, err)
	return msg, err
}

// Sources lists the configured sources and how far each has been swept.
func (c Client) Sources() (string, error) {
	start := time.Now()
	p := c.Context()
	srcs, cfgErr := source.Load(c.Home)
	marks, err := Marks(c.Home)
	c.log(p, "sources", nil, "", "", start, err)
	if err != nil {
		return "", err
	}
	var infos []SourceInfo
	for _, src := range srcs {
		infos = append(infos, sourceInfo(src, marks))
	}
	return FormatSources(source.ConfigPath(c.Home), infos, cfgErr), nil
}

// AddSource adds a source to sources.json, or points an existing one at
// another folder. Either way it starts with the sessions that change from now
// on. With no sources.json yet, the defaults are written out first.
func (c Client) AddSource(kind, dir string) (string, error) {
	start := time.Now()
	msg, err := c.editSources(func(cfg *source.Config) (string, error) {
		if dir != "" && !strings.HasPrefix(dir, "~") {
			abs, err := filepath.Abs(dir)
			if err != nil {
				return "", err
			}
			dir = abs
		}
		spec := source.Spec{Kind: kind, Dir: dir, Added: start.UTC()}
		if _, err := source.New(spec); err != nil {
			return "", err
		}
		for i, s := range cfg.Sources {
			if s.Kind != kind {
				continue
			}
			if s.Dir == dir {
				return fmt.Sprintf("%s is already a source.", kind), nil
			}
			cfg.Sources[i] = spec
			return fmt.Sprintf("Pointed %s at %s; it reads sessions that change from now on.", kind, orDefault(dir, "its default folder")), nil
		}
		cfg.Sources = append(cfg.Sources, spec)
		return fmt.Sprintf("Added %s; it reads sessions that change from now on.", kind), nil
	})
	c.log(c.Context(), "source_add", nil, "", kind, start, err)
	return msg, err
}

// RemoveSource takes a source out of sources.json. Its sessions are no longer
// swept; what was learned from them stays.
func (c Client) RemoveSource(kind string) (string, error) {
	start := time.Now()
	msg, err := c.editSources(func(cfg *source.Config) (string, error) {
		for i, s := range cfg.Sources {
			if s.Kind == kind {
				cfg.Sources = append(cfg.Sources[:i], cfg.Sources[i+1:]...)
				return fmt.Sprintf("Removed %s; its sessions are no longer swept, and its memories stay.", kind), nil
			}
		}
		return "", fmt.Errorf("no source %q", kind)
	})
	c.log(c.Context(), "source_remove", nil, "", kind, start, err)
	return msg, err
}

func (c Client) editSources(f func(*source.Config) (string, error)) (string, error) {
	unlock, err := lockFile(filepath.Join(c.Home, "sources.lock"), true)
	if err != nil {
		return "", err
	}
	defer unlock()
	cfg, err := source.LoadConfig(c.Home)
	if err != nil {
		return "", err
	}
	cfg.Sources = slices.Clone(cfg.Sources)
	note, err := f(&cfg)
	if err != nil {
		return "", err
	}
	if err := source.SaveConfig(c.Home, cfg); err != nil {
		return "", err
	}
	list, err := c.Sources()
	return note + "\n\n" + list, err
}

// LogFilter narrows the activity log.
type LogFilter struct {
	Session string // prefix
	Kind    string
	Project string
	Last    int // 0: all
}

// Activity returns the activity log, oldest first, filtered.
func (c Client) Activity(f LogFilter) ([]Event, error) {
	evs, err := c.Context().Events()
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range evs {
		if f.Match(e) {
			out = append(out, e)
		}
	}
	if f.Last > 0 && len(out) > f.Last {
		out = out[len(out)-f.Last:]
	}
	return out, nil
}

func (f LogFilter) Match(e Event) bool {
	return strings.HasPrefix(e.Session, f.Session) && (f.Kind == "" || e.Kind == f.Kind) && (f.Project == "" || e.Project == f.Project)
}

func FormatEvents(evs []Event) string {
	if len(evs) == 0 {
		return "No activity.\n"
	}
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.String() + "\n")
	}
	return b.String()
}

func (c Client) log(p Project, op string, files []string, query, note string, start time.Time, err error) {
	p.Log(Event{Kind: "call", Agent: c.Caller, Name: op, Files: files, Prompt: query, Note: note, Ms: time.Since(start).Milliseconds(), Err: errString(err)})
}
