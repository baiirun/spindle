package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"spindle/internal/memory"
)

// fixture makes a home with global, two projects and an activity log.
func fixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	write := func(store, name, typ, desc, body string, at time.Time) {
		t.Helper()
		s := memory.Store(home, store)
		if _, err := s.Apply([]memory.Op{{Op: "write", Name: name, Type: typ, Description: desc, Body: body}}, "cli", "", at); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(s.MemDir(), name+".md"), at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("global", "role", "user", "Byron writes Go", "Go on macOS.", now)
	write("spindle", "selector-model", "project", "Which model picks recalls", "Sonnet picks.", now.Add(time.Hour))
	write("spindle", "selector-model", "project", "Which model picks recalls", "Sonnet picks, low effort.", now.Add(3*time.Hour))
	write("spindle", "commit-style", "directive", "Short commit messages", "Keep them short.", now.Add(2*time.Hour))
	write("zaum", "vault", "reference", "Where the vault lives", "In Notion.", now.Add(4*time.Hour))

	p := memory.Store(home, "spindle")
	for _, e := range []memory.Event{
		{At: now, Kind: "recall", Files: []string{"spindle/selector-model.md", "global/role.md"}},
		{At: now.Add(time.Minute), Kind: "extract", Files: []string{"+spindle/selector-model"}},
		{At: now.Add(2 * time.Minute), Kind: "recall", Project: "spindle", Files: []string{"selector-model.md"}}, // older, bare
		{At: now.Add(3 * time.Minute), Kind: "recall", Project: "zaum", Files: []string{"selector-model.md"}},    // bare, other project
		{At: now.Add(4 * time.Minute), Kind: "call", Name: "read", Files: []string{"zaum/vault.md"}},
	} {
		p.Log(e)
	}
	return home
}

func TestLoad(t *testing.T) {
	home := fixture(t)
	s, err := Load(home, "spindle")
	if err != nil {
		t.Fatal(err)
	}
	type group struct {
		name     string
		context  bool
		memories []string
	}
	want := []group{
		{"global", false, []string{"role"}},
		{"spindle", true, []string{"selector-model", "commit-style"}}, // newest first
		{"zaum", false, []string{"vault"}},
	}
	var got []group
	for _, g := range s.Groups {
		var names []string
		for _, m := range g.Memories {
			names = append(names, m.Name)
		}
		got = append(got, group{g.Name, g.Context, names})
	}
	if !slices.EqualFunc(got, want, func(a, b group) bool {
		return a.name == b.name && a.context == b.context && slices.Equal(a.memories, b.memories)
	}) {
		t.Errorf("groups = %+v, want %+v", got, want)
	}
	if len(s.Events) != 5 {
		t.Errorf("%d events, want 5", len(s.Events))
	}
}

func TestItemsAndGroup(t *testing.T) {
	s, err := Load(fixture(t), "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		group string
		want  []string
	}{
		{"global", []string{"global/role"}},
		{"spindle", []string{"spindle/selector-model", "spindle/commit-style"}},
		{"nope", nil},
	}
	for _, tt := range tests {
		t.Run(tt.group, func(t *testing.T) {
			if got := refs(s.Items(s.Group(tt.group))); !slices.Equal(got, tt.want) {
				t.Errorf("Items(%s) = %v, want %v", tt.group, got, tt.want)
			}
		})
	}
}

func TestSearch(t *testing.T) {
	s, err := Load(fixture(t), "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"zaum/vault", "spindle/selector-model", "spindle/commit-style", "global/role"}},
		{"commit", []string{"spindle/commit-style"}},
		{"notion", []string{"zaum/vault"}},                   // body only
		{"model effort", []string{"spindle/selector-model"}}, // name and body
		{"nothing-matches-this", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := refs(s.Search(tt.query, 10)); !slices.Equal(got, tt.want) {
				t.Errorf("Search(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestHistory(t *testing.T) {
	home := fixture(t)
	tests := []struct {
		item Item
		want []string // the new body of each change, newest first
	}{
		{Item{Project: "spindle", Memory: memory.Memory{Name: "selector-model"}}, []string{"Sonnet picks, low effort.", "Sonnet picks."}},
		{Item{Project: "spindle", Memory: memory.Memory{Name: "commit-style"}}, []string{"Keep them short."}},
		{Item{Project: "global", Memory: memory.Memory{Name: "selector-model"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.item.Ref(), func(t *testing.T) {
			hist, err := History(home, tt.item)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, c := range hist {
				m, _ := memory.Parse(c.New)
				got = append(got, m.Body)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("History = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivity(t *testing.T) {
	s, err := Load(fixture(t), "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		item    Item
		kinds   []string // newest first
		recalls int
	}{
		{Item{Project: "spindle", Memory: memory.Memory{Name: "selector-model"}}, []string{"recall", "extract", "recall"}, 2},
		{Item{Project: "global", Memory: memory.Memory{Name: "role"}}, []string{"recall"}, 1},
		{Item{Project: "zaum", Memory: memory.Memory{Name: "vault"}}, []string{"call"}, 0},
		{Item{Project: "spindle", Memory: memory.Memory{Name: "commit-style"}}, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.item.Ref(), func(t *testing.T) {
			evs := s.Activity(tt.item)
			var kinds []string
			for _, e := range evs {
				kinds = append(kinds, e.Kind)
			}
			if !slices.Equal(kinds, tt.kinds) {
				t.Errorf("kinds = %v, want %v", kinds, tt.kinds)
			}
			if n := Recalls(evs); n != tt.recalls {
				t.Errorf("Recalls = %d, want %d", n, tt.recalls)
			}
		})
	}
}

// TestKeys drives the model with keys and checks what each pane is on.
func TestKeys(t *testing.T) {
	home := fixture(t)
	tests := []struct {
		name    string
		keys    []string
		item    string // the selected memory
		preview string // a line the preview has
	}{
		{"starts on the project", nil, "spindle/selector-model", "Sonnet picks, low effort."},
		{"moves down", []string{"tab", "j"}, "spindle/commit-style", "Keep them short."},
		{"changes project", []string{"j"}, "zaum/vault", "In Notion."},
		{"shows history", []string{"h"}, "spindle/selector-model", "changed by cli"},
		{"shows activity", []string{"a"}, "spindle/selector-model", "3 events, 2 recalls"},
		{"searches", []string{"/", "n", "o", "t", "i", "o", "n", "enter"}, "zaum/vault", "In Notion."},
		{"ends a search", []string{"/", "r", "o", "l", "e", "esc"}, "spindle/selector-model", "Sonnet picks, low effort."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m tea.Model = New(home, "spindle", "spindle")
			m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			for _, k := range tt.keys {
				m, _ = m.Update(keyMsg(k))
			}
			got := m.(Model)
			it, _ := got.current()
			if it.Ref() != tt.item {
				t.Errorf("item = %s, want %s", it.Ref(), tt.item)
			}
			if !strings.Contains(strings.Join(got.preview, "\n"), tt.preview) {
				t.Errorf("preview has no %q:\n%s", tt.preview, strings.Join(got.preview, "\n"))
			}
			if v := got.View(); strings.Count(v, "\n") != got.contentHeight() {
				t.Errorf("view is %d lines, want %d", strings.Count(v, "\n")+1, got.contentHeight()+1)
			}
		})
	}
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func refs(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Ref())
	}
	return out
}
