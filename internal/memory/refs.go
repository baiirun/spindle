package memory

import (
	"os"
	"sort"
	"strings"
	"time"
)

// Everywhere a memory is shown it is named <project>/<name>: global/role,
// spindle/selector-model. A bare name is looked up in the session's project,
// then in global, then in any project that has it.

// RefOf is how memory name of store s is named everywhere.
func RefOf(s Project, name string) string { return s.Name + "/" + name }

// splitRef splits "<project>/<name>"; store is "" for a bare name.
func splitRef(ref string) (store, name string) {
	ref = strings.TrimSuffix(strings.TrimSpace(ref), ".md")
	if s, n, ok := strings.Cut(ref, "/"); ok {
		return NameOf(s), NameOf(n)
	}
	return "", NameOf(ref)
}

// bare is a memory with its store prefix taken off the name, for rendering
// its file text.
func bare(m Memory) Memory {
	_, m.Name = splitRef(m.Name)
	return m
}

// locate finds the store a reference points to. For a bare name that exists
// nowhere it is the session's project (global if there is none).
func (p Project) locate(ref string) (Project, string) {
	store, name := splitRef(ref)
	if store != "" {
		return Store(p.Home, store), name
	}
	if s, ok := p.find(name); ok {
		return s, name
	}
	if p.Name != "" {
		return p, name
	}
	return Store(p.Home, GlobalName), name
}

// find looks for a bare name in the session's project, then global, then any
// project that has it.
func (p Project) find(name string) (Project, bool) {
	var tries []Project
	if p.Name != "" {
		tries = append(tries, p)
	}
	tries = append(tries, Store(p.Home, GlobalName))
	if all, err := Projects(p.Home); err == nil {
		tries = append(tries, all...)
	}
	for _, s := range tries {
		if _, ok := s.Get(name); ok {
			return s, true
		}
	}
	return Project{}, false
}

// locateLogged is locate for a memory's history: a bare name that exists
// nowhere (it was deleted) is looked up, in the same order, in each store's
// change log.
func (p Project) locateLogged(ref string) (Project, string) {
	s, name := p.locate(ref)
	if store, _ := splitRef(ref); store != "" {
		return s, name
	}
	if _, ok := s.Get(name); ok {
		return s, name
	}
	var tries []Project
	if p.Name != "" {
		tries = append(tries, p)
	}
	tries = append(tries, Store(p.Home, GlobalName))
	if all, err := Projects(p.Home); err == nil {
		tries = append(tries, all...)
	}
	for _, t := range tries {
		changes, _ := t.Changes()
		for _, c := range changes {
			if c.Name == name {
				return t, name
			}
		}
	}
	return s, name
}

// GetRef reads one memory from any store; its Name is the reference.
func (p Project) GetRef(ref string) (Memory, bool) {
	s, name := p.locate(ref)
	m, ok := s.Get(name)
	m.Name = RefOf(s, name)
	return m, ok
}

// LoadAll reads every store's memories, newest first, named by reference.
func (p Project) LoadAll() ([]Memory, error) {
	all, err := stores(p.Home)
	if err != nil {
		return nil, err
	}
	var mems []Memory
	for _, s := range all {
		ms, err := s.Load()
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			m.Name = RefOf(s, m.Name)
			mems = append(mems, m)
		}
	}
	sort.SliceStable(mems, func(i, j int) bool { return mems[i].Modified.After(mems[j].Modified) })
	return mems, nil
}

// route picks the store a write or delete goes to, and strips the name to
// its bare form. In order: a project in the name or in o.Project; the store
// that already has the name (this project's, then global, then any other
// project, as locate looks it up); global for type user; this project. With
// no project for the folder, that last step fails.
func (p Project) route(o Op) (Project, Op, error) {
	store, name := splitRef(o.Name)
	o.Name = name
	if store == "" {
		store = NameOf(o.Project)
	}
	if store != "" {
		return Store(p.Home, store), o, nil
	}
	if o.Op == "delete" {
		s, _ := p.locate(name)
		return s, o, nil
	}
	if s, ok := p.find(name); ok {
		return s, o, nil
	}
	if strings.TrimSpace(o.Type) == "user" {
		return Store(p.Home, GlobalName), o, nil
	}
	if p.Name != "" {
		return p, o, nil
	}
	return Project{}, o, errNoProject
}

// ApplyAll applies ops to the store each belongs to, taking each store's lock
// in turn (never two at once). A write to a project that doesn't exist
// creates it, unlinked: only an explicit link (memory_project, spin project)
// ties a folder to a project, since a write can be about any project. Ops
// that can't be routed are skipped.
func (p Project) ApplyAll(ops []Op, by, session string, now time.Time) (Result, error) {
	var res Result
	groups := map[string][]Op{}
	var order []Project
	for _, o := range ops {
		s, o, err := p.route(o)
		if err != nil {
			res.Skipped++
			continue
		}
		if _, ok := groups[s.Name]; !ok {
			order = append(order, s)
		}
		groups[s.Name] = append(groups[s.Name], o)
	}
	for _, s := range order {
		created := !s.Exists()
		r, err := s.applyLocked(groups[s.Name], by, session, now)
		res.Written += r.Written
		res.Deleted += r.Deleted
		res.Skipped += r.Skipped
		for _, c := range r.Changed {
			res.Changed = append(res.Changed, c[:1]+RefOf(s, c[1:]))
		}
		if err != nil {
			return res, err
		}
		if created && r.Written > 0 && !s.IsGlobal() {
			desc := ""
			for _, o := range groups[s.Name] {
				if d := oneLine(o.ProjectDescription); d != "" {
					desc = d
				}
			}
			if desc != "" {
				_ = s.UpdateMeta(func(m *Meta) { m.Description = desc })
			}
		}
	}
	return res, nil
}

func (s Project) applyLocked(ops []Op, by, session string, now time.Time) (Result, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Result{}, err
	}
	unlock, err := s.Lock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	return s.Apply(ops, by, session, now)
}

// RefIndex is the store's MEMORY.md with links named as references.
func (p Project) RefIndex() string { return refIndex(p, p.ReadIndex()) }

// refIndex names the links of index text of store s as references.
func refIndex(s Project, index string) string {
	return strings.ReplaceAll(index, "](", "]("+s.Name+"/")
}

// wroteSince reports whether by wrote to any store after t.
func (p Project) wroteSince(by string, t time.Time) bool {
	all, _ := stores(p.Home)
	for _, s := range all {
		changes, _ := s.Changes()
		for i := len(changes) - 1; i >= 0 && !changes[i].At.Before(t); i-- {
			if changes[i].By == by {
				return true
			}
		}
	}
	return false
}
