package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"spindle/internal/memory"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// fixture is two projects: spindle with a rule, a page and two entries, and
// zaum with one page.
func fixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	sp, zaum := memory.Store(home, "spindle"), memory.Store(home, "zaum")
	add := func(st memory.Project, id, gist string, at time.Time) {
		g, _ := memory.ParseGist(gist)
		if err := st.Append([]memory.Entry{{ID: memory.EntryID(id), At: at, Session: "s", Source: memory.Pointer{Kind: memory.FromCLI}, Gist: g}}); err != nil {
			t.Fatal(err)
		}
	}
	add(sp, "e-1", "Byron wants short commit titles.", t0)
	add(sp, "e-2", "Rollups are written by a side model.", t0.Add(time.Hour))
	rule := memory.Rule{Page: memory.Page{Slug: "commit-style", Title: "Commit style", Body: "- Keep them short.\n", Evidence: []memory.EntryID{"e-1"}}}
	page := memory.WikiPage{Page: memory.Page{Slug: "vault", Title: "Zaum vault", Body: "In Notion.\n"}, Kind: memory.KindTool}
	if _, err := sp.ApplyEdits([]memory.Edit{{Target: memory.PageRef{Layer: memory.RulesLayer, Slug: "commit-style"}, After: rule.Render(), Reason: "stated"}}, memory.ByCLI, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := zaum.ApplyEdits([]memory.Edit{{Target: memory.PageRef{Layer: memory.WikiLayer, Slug: "vault"}, After: page.Render(), Reason: "where it lives"}}, memory.ByCLI, t0); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestKeys drives the model with keys and checks what each pane is on.
func TestKeys(t *testing.T) {
	home := fixture(t)
	tests := []struct {
		name    string
		keys    []string
		item    string // the selected item
		preview string // a line the preview has
	}{
		{"starts on the project's first rule", nil, "spindle/rules/commit-style", "Keep them short."},
		{"rules show what they rest on", nil, "spindle/rules/commit-style", "e-1"},
		{"moves down to the newest entry", []string{"tab", "j"}, "spindle/e-2", "side model"},
		{"changes store", []string{"j"}, "zaum/wiki/vault", "In Notion."},
		{"shows history", []string{"h"}, "spindle/rules/commit-style", "created by cli: stated"},
		{"searches", []string{"/", "n", "o", "t", "i", "o", "n", "enter"}, "zaum/wiki/vault", "In Notion."},
		{"ends a search", []string{"/", "r", "o", "l", "l", "esc"}, "spindle/rules/commit-style", "Keep them short."},
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
