// Package tui is spin tui: a read-only terminal viewer for memory, with
// projects on the left, the selected project's memories in the middle and one
// memory's body, history or activity on the right. It follows prog's TUI
// (Bubble Tea and Lip Gloss): one Model, keys handled in Update, panes drawn
// line by line in View.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"spindle/internal/memory"
)

type pane int

const (
	paneProjects pane = iota
	paneMemories
	panePreview
)

type previewMode int

const (
	showBody previewMode = iota
	showHistory
	showActivity
)

const (
	searchLimit   = 100
	projectsWidth = 24
	minSplitWidth = 80 // narrower terminals show only the focused pane
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	headStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("238"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	inputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	sepStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	typeColors    = map[string]lipgloss.Color{"user": "141", "directive": "214", "project": "42", "reference": "39"}
)

// Model is the viewer's state.
type Model struct {
	home string
	snap Snapshot
	err  error

	group int    // cursor in the projects pane
	items []Item // the middle pane: the group's memories, or search results
	item  int    // cursor in items
	focus pane
	mode  previewMode

	query     string // search; items are its results while searching
	searching bool   // results shown
	typing    bool   // the query is being typed

	preview []string // the preview's text, unwrapped, for the current item and mode
	scroll  int      // first preview line shown

	width, height int
}

// New is a viewer of the stores under home, starting on the named project
// (or global when it has none). context marks the folder's project.
func New(home, start, context string) Model {
	m := Model{home: home}
	m.load(context)
	if i := m.snap.Group(start); i >= 0 {
		m.group = i
	}
	m.showGroup()
	return m
}

// Run starts the viewer full screen.
func Run(home, start, context string) error {
	_, err := tea.NewProgram(New(home, start, context), tea.WithAltScreen()).Run()
	return err
}

func (m *Model) load(context string) {
	m.snap, m.err = Load(m.home, context)
}

// reload rereads the stores, keeping the selection where it still exists.
func (m *Model) reload() {
	groupName, ref := "", ""
	if m.group < len(m.snap.Groups) {
		groupName = m.snap.Groups[m.group].Name
	}
	if it, ok := m.current(); ok {
		ref = it.Ref()
	}
	context := ""
	for _, g := range m.snap.Groups {
		if g.Context {
			context = g.Name
		}
	}
	m.load(context)
	m.group = max(m.snap.Group(groupName), 0)
	if m.searching {
		m.items = m.snap.Search(m.query, searchLimit)
	} else {
		m.items = m.snap.Items(m.group)
	}
	m.item = 0
	for i, it := range m.items {
		if it.Ref() == ref {
			m.item = i
		}
	}
	m.refreshPreview()
}

func (m Model) current() (Item, bool) {
	if m.item < 0 || m.item >= len(m.items) {
		return Item{}, false
	}
	return m.items[m.item], true
}

func (m *Model) showGroup() {
	m.searching, m.typing, m.query = false, false, ""
	m.items, m.item = m.snap.Items(m.group), 0
	m.refreshPreview()
}

func (m *Model) search() {
	m.items, m.item = m.snap.Search(m.query, searchLimit), 0
	m.refreshPreview()
}

// refreshPreview rebuilds the preview text for the current item and mode.
func (m *Model) refreshPreview() {
	m.scroll = 0
	it, ok := m.current()
	if !ok {
		m.preview = []string{dimStyle.Render("No memory selected.")}
		return
	}
	mem := it.Memory
	head := []string{
		headStyle.Render(it.Ref()),
		typeStyle(mem.Type).Render(orDash(mem.Type)) + dimStyle.Render("  modified "+mem.Modified.Local().Format("2006-01-02 15:04")),
		mem.Description,
		"",
		m.tabs(),
		"",
	}
	var body string
	switch m.mode {
	case showBody:
		body = mem.Body
	case showHistory:
		hist, err := History(m.home, it)
		if err != nil {
			body = errorStyle.Render("history: " + err.Error())
		} else {
			body = memory.FormatHistory(it.Ref(), hist, true)
		}
	case showActivity:
		evs := m.snap.Activity(it)
		if len(evs) == 0 {
			body = "No activity names this memory."
		} else {
			lines := []string{dimStyle.Render(fmt.Sprintf("%d events, %d recalls; newest first", len(evs), Recalls(evs))), ""}
			for _, e := range evs {
				lines = append(lines, e.String())
			}
			body = strings.Join(lines, "\n")
		}
	}
	m.preview = append(head, strings.Split(strings.TrimRight(body, "\n"), "\n")...)
}

func (m Model) tabs() string {
	var out []string
	for i, name := range []string{"[b]ody", "[h]istory", "[a]ctivity"} {
		if previewMode(i) == m.mode {
			out = append(out, selectedStyle.Render(" "+name+" "))
		} else {
			out = append(out, dimStyle.Render(" "+name+" "))
		}
	}
	return strings.Join(out, " ")
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.typing {
			return m.typeKey(msg), nil
		}
		return m.key(msg)
	}
	return m, nil
}

// typeKey edits the search query; results follow each key.
func (m Model) typeKey(msg tea.KeyMsg) Model {
	switch msg.Type {
	case tea.KeyEsc:
		m.showGroup()
	case tea.KeyEnter:
		m.typing = false
		m.focus = paneMemories
	case tea.KeyBackspace:
		if r := []rune(m.query); len(r) > 0 {
			m.query = string(r[:len(r)-1])
			m.search()
		}
	case tea.KeyRunes, tea.KeySpace:
		m.query += string(msg.Runes)
		m.search()
	}
	return m
}

func (m Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "tab", "right", "enter":
		m.focus = min(m.focus+1, panePreview)
	case "shift+tab", "left":
		m.focus = max(m.focus-1, paneProjects)
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+u":
		m.move(-max(m.contentHeight()/2, 1))
	case "pgdown", "ctrl+d", " ":
		m.move(max(m.contentHeight()/2, 1))
	case "g", "home":
		m.move(-1 << 30)
	case "G", "end":
		m.move(1 << 30)
	case "b":
		m.setMode(showBody)
	case "h":
		m.setMode(showHistory)
	case "a":
		m.setMode(showActivity)
	case "/":
		m.typing, m.searching, m.query = true, true, ""
		m.focus = paneMemories
		m.search()
	case "esc":
		if m.searching {
			m.showGroup()
		}
	case "r":
		m.reload()
	}
	return m, nil
}

func (m *Model) setMode(mode previewMode) {
	m.mode = mode
	m.refreshPreview()
}

// move moves the focused pane's cursor, or scrolls the preview, by d.
func (m *Model) move(d int) {
	switch m.focus {
	case paneProjects:
		g := clamp(m.group+d, 0, len(m.snap.Groups)-1)
		if g != m.group || m.searching {
			m.group = g
			m.showGroup()
		}
	case paneMemories:
		i := clamp(m.item+d, 0, len(m.items)-1)
		if i != m.item {
			m.item = i
			m.refreshPreview()
		}
	case panePreview:
		m.scroll = clamp(m.scroll+d, 0, max(len(m.wrapped(m.previewWidth()))-m.contentHeight(), 0))
	}
}

func (m Model) contentHeight() int { return max(m.height-1, 3) } // one line for the footer

// widths of the three panes; a zero width hides the pane.
func (m Model) widths() (projects, memories, preview int) {
	if m.width < minSplitWidth {
		w := [3]int{}
		w[m.focus] = m.width
		return w[0], w[1], w[2]
	}
	avail := m.width - 2 // two separators
	projects = projectsWidth
	memories = (avail - projects) * 2 / 5
	return projects, memories, avail - projects - memories
}

func (m Model) previewWidth() int {
	_, _, w := m.widths()
	return max(w-2, 10)
}

func (m Model) wrapped(width int) []string {
	var out []string
	for _, l := range m.preview {
		out = append(out, strings.Split(ansi.Wrap(l, width, ""), "\n")...)
	}
	return out
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	h := m.contentHeight()
	pw, mw, vw := m.widths()
	var cols [][]string
	if pw > 0 {
		cols = append(cols, fit(m.projectsPane(pw, h), pw, h))
	}
	if mw > 0 {
		cols = append(cols, fit(m.memoriesPane(mw, h), mw, h))
	}
	if vw > 0 {
		cols = append(cols, fit(m.previewPane(vw, h), vw, h))
	}
	var b strings.Builder
	sep := sepStyle.Render("│")
	for i := 0; i < h; i++ {
		for j, c := range cols {
			if j > 0 {
				b.WriteString(sep)
			}
			b.WriteString(c[i])
		}
		b.WriteString("\n")
	}
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) title(p pane, text string) string {
	if m.focus == p {
		return titleStyle.Render(text)
	}
	return headStyle.Render(text)
}

func (m Model) projectsPane(w, h int) []string {
	lines := []string{m.title(paneProjects, " Projects"), ""}
	if m.err != nil {
		return append(lines, errorStyle.Render(" "+m.err.Error()))
	}
	var rows []string
	for i, g := range m.snap.Groups {
		mark := " "
		if g.Context {
			mark = "*"
		}
		count := fmt.Sprintf("%d", len(g.Memories))
		name := ansi.Truncate(g.Name, w-len(count)-4, "…")
		row := mark + name + strings.Repeat(" ", max(w-2-lipgloss.Width(name)-len(count), 1)) + count + " "
		switch {
		case i == m.group && m.focus == paneProjects && !m.searching:
			row = selectedStyle.Render(row)
		case i == m.group && !m.searching:
			row = cursorStyle.Render(row)
		}
		rows = append(rows, row)
	}
	return append(lines, window(rows, m.group, 1, h-len(lines))...)
}

func (m Model) memoriesPane(w, h int) []string {
	var head string
	switch {
	case m.searching:
		head = fmt.Sprintf(" Search %q: %d", m.query, len(m.items))
	case m.group < len(m.snap.Groups):
		g := m.snap.Groups[m.group]
		head = " " + g.Name
		if g.Description != "" {
			head += dimStyle.Render(" · " + g.Description)
		}
	}
	lines := []string{ansi.Truncate(m.title(paneMemories, head), w, "…"), ""}
	if len(m.items) == 0 {
		return append(lines, dimStyle.Render(" No memories."))
	}
	var rows []string
	for i, it := range m.items {
		mem := it.Memory
		name := mem.Name
		if m.searching {
			name = it.Ref()
		}
		date := mem.Modified.Local().Format("2006-01-02")
		name = ansi.Truncate(name, w-len(date)-3, "…")
		top := " " + name + strings.Repeat(" ", max(w-2-lipgloss.Width(name)-len(date), 1)) + date + " "
		bottom := " " + ansi.Truncate(orDash(mem.Type)+" · "+oneLine(mem.Description), w-2, "…")
		switch {
		case i == m.item && m.focus == paneMemories:
			top, bottom = selectedStyle.Render(top), selectedStyle.Render(padTo(bottom, w))
		case i == m.item:
			top, bottom = cursorStyle.Render(top), cursorStyle.Render(padTo(bottom, w))
		default:
			bottom = dimStyle.Render(bottom)
		}
		rows = append(rows, top, bottom)
	}
	return append(lines, window(rows, m.item, 2, h-len(lines))...)
}

func (m Model) previewPane(w, h int) []string {
	text := m.wrapped(max(w-2, 10))
	start := clamp(m.scroll, 0, max(len(text)-h, 0))
	end := min(start+h, len(text))
	out := make([]string, 0, h)
	for _, l := range text[start:end] {
		out = append(out, " "+l)
	}
	if m.focus == panePreview && len(out) > 0 {
		out[0] = titleStyle.Render("▌") + strings.TrimPrefix(out[0], " ")
	}
	return out
}

func (m Model) footer() string {
	if m.typing {
		return inputStyle.Render("/" + m.query + "█")
	}
	keys := "←/→ pane  ↑/↓ move  b/h/a body/history/activity  / search  r reload  q quit"
	if m.searching {
		keys = "esc end search  " + keys
	}
	return dimStyle.Render(ansi.Truncate(keys, m.width, "…"))
}

// window is the slice of rows, each item rowsPer rows tall, that keeps the
// cursor's item on screen in h rows.
func window(rows []string, cursor, rowsPer, h int) []string {
	if h <= 0 {
		return nil
	}
	fits := max(h/rowsPer, 1)
	first := 0
	if cursor >= fits {
		first = cursor - fits + 1
	}
	start := min(first*rowsPer, len(rows))
	return rows[start:min(start+fits*rowsPer, len(rows))]
}

// fit makes lines exactly h lines of exactly width w.
func fit(lines []string, w, h int) []string {
	out := make([]string, h)
	for i := range out {
		if i < len(lines) {
			out[i] = padTo(ansi.Truncate(lines[i], w, ""), w)
		} else {
			out[i] = strings.Repeat(" ", w)
		}
	}
	return out
}

func padTo(s string, w int) string {
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

func typeStyle(t string) lipgloss.Style {
	if c, ok := typeColors[t]; ok {
		return lipgloss.NewStyle().Bold(true).Foreground(c)
	}
	return dimStyle
}

func clamp(x, lo, hi int) int { return max(lo, min(x, hi)) }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
