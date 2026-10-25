package memory

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Two scopes. A project's memories load only in that project; global
// memories (~/.spindle/global) load in every project: who the user is and
// rules they want everywhere. Everywhere a memory is named, a global one is
// "global/<name>". A write with no scope goes where the name already exists,
// else to global for type user and to the project for the rest.

const (
	GlobalPrefix = "global/"
	globalRoot   = "(global)"
)

// Global is the store shared by every project.
func (p Project) Global() Project {
	return Project{Root: globalRoot, Dir: filepath.Join(p.Home, "global"), Home: p.Home}
}

func (p Project) IsGlobal() bool { return p.Root == globalRoot }

// ref resolves a memory name to its store and bare name.
func (p Project) ref(name string) (Project, string) {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, GlobalPrefix) {
		return p.Global(), NameOf(strings.TrimPrefix(name, GlobalPrefix))
	}
	return p, NameOf(name)
}

// RefOf is how a memory of store s is named everywhere.
func RefOf(s Project, name string) string {
	if s.IsGlobal() {
		return GlobalPrefix + name
	}
	return name
}

// LoadAll reads the project's and the global memories, newest first, with
// global names prefixed.
func (p Project) LoadAll() ([]Memory, error) {
	mems, err := p.Load()
	if err != nil || p.IsGlobal() {
		return mems, err
	}
	glob, err := p.Global().Load()
	if err != nil {
		return nil, err
	}
	for _, m := range glob {
		m.Name = GlobalPrefix + m.Name
		mems = append(mems, m)
	}
	sort.SliceStable(mems, func(i, j int) bool { return mems[i].Modified.After(mems[j].Modified) })
	return mems, nil
}

// GetRef reads one memory of either scope; its Name is the reference.
func (p Project) GetRef(name string) (Memory, bool) {
	s, bare := p.ref(name)
	m, ok := s.Get(bare)
	m.Name = RefOf(s, bare)
	return m, ok
}

// scopeFor picks the store a write or delete goes to.
func (p Project) scopeFor(o Op) (Project, Op) {
	if strings.HasPrefix(strings.TrimSpace(o.Name), GlobalPrefix) || o.Scope == "global" {
		o.Name = strings.TrimPrefix(strings.TrimSpace(o.Name), GlobalPrefix)
		return p.Global(), o
	}
	if o.Scope == "project" || o.Op != "write" {
		return p, o
	}
	name := NameOf(o.Name)
	if _, ok := p.Get(name); ok {
		return p, o
	}
	if _, ok := p.Global().Get(name); ok || strings.TrimSpace(o.Type) == "user" {
		return p.Global(), o
	}
	return p, o
}

// ApplyAll applies ops to whichever store each belongs to. Callers hold the
// project lock; the global store's lock is taken here when needed.
func (p Project) ApplyAll(ops []Op, by, session string, now time.Time) (Result, error) {
	var local, global []Op
	for _, o := range ops {
		s, o := p.scopeFor(o)
		if s.IsGlobal() && !p.IsGlobal() {
			global = append(global, o)
		} else {
			local = append(local, o)
		}
	}
	res, err := p.Apply(local, by, session, now)
	if err != nil || len(global) == 0 {
		return res, err
	}
	g := p.Global()
	unlock, err := g.Lock()
	if err != nil {
		return res, err
	}
	defer unlock()
	gres, err := g.Apply(global, by, session, now)
	res.Written += gres.Written
	res.Deleted += gres.Deleted
	res.Skipped += gres.Skipped
	for _, c := range gres.Changed {
		res.Changed = append(res.Changed, c[:1]+GlobalPrefix+c[1:])
	}
	return res, err
}

// GlobalIndex is the global MEMORY.md with links named as references.
func (p Project) GlobalIndex() string {
	if p.IsGlobal() {
		return ""
	}
	return strings.ReplaceAll(p.Global().ReadIndex(), "](", "]("+GlobalPrefix)
}

// changesSince reports whether by wrote to either store after t.
func (p Project) wroteSince(by string, t time.Time) bool {
	for _, s := range []Project{p, p.Global()} {
		changes, _ := s.Changes()
		for i := len(changes) - 1; i >= 0 && !changes[i].At.Before(t); i-- {
			if changes[i].By == by {
				return true
			}
		}
		if p.IsGlobal() {
			break
		}
	}
	return false
}
