package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mikolajwichrowski/baitai/internal/office"
	"github.com/mikolajwichrowski/baitai/internal/workspace"
)

func (m *model) render(e *office.Entry, showWhere bool) string {
	label := pillSt.Background(lipgloss.Color(kindColor(e))).Render(e.Who)
	if e.Kind == office.KindThinking {
		label = fg(cMuted).Italic(true).Render("💭 " + e.Who)
	}
	if showWhere && e.Where != "" {
		label += dimSt.Render("  " + e.Where)
	}
	if e.Text == "" {
		return "\n  " + label + "\n\n  " + m.sp.View() + dimSt.Render(" working…") + "\n"
	}
	if e.Kind == office.KindThinking { // reasoning stays plain and quiet
		return "\n  " + label + "\n" + fg(cMuted).Italic(true).Width(m.vp.Width()-6).PaddingLeft(2).Render(e.Text) + "\n"
	}
	c, ok := m.cache[e]
	if !ok || c.n != len(e.Text) {
		c = cached{n: len(e.Text), out: e.Text}
		if out, err := m.md.Render(e.Text); err == nil {
			c.out = out
		}
		m.cache[e] = c
	}
	return "\n  " + label + "\n" + c.out
}

// refresh rebuilds the selected view; jump forces scrolling to the bottom.
func (m *model) refresh(jump bool) {
	if m.md == nil {
		return
	}
	if m.sel == "files" {
		m.setContent(m.filesView(), "", jump)
		return
	}
	o, a, f := m.o, m.agent(), m.forum()
	o.Lock()
	var b strings.Builder
	empty := "nothing here yet"
	switch {
	case m.sel == "boss":
		for _, e := range o.Boss.Log {
			b.WriteString(m.render(e, false))
		}
		o.Boss.Unread = false
	case m.sel == "all":
		for _, e := range o.Feed {
			b.WriteString(m.render(e, true))
		}
		empty = "the office is quiet"
	case f != nil:
		for _, t := range f.Threads {
			state := fg(cGreen).Render("open")
			if t.Closed {
				state = dimSt.Render("closed")
			}
			if t.Running {
				state = m.sp.View() + " " + fg(cAmber).Render("working")
			}
			fmt.Fprintf(&b, "\n  %s  %s  %s\n", fg(cWhite).Bold(true).Render(fmt.Sprintf("#%d %s", t.ID, t.Title)), state, dimSt.Render("by "+t.Owner))
			for _, e := range t.Posts {
				b.WriteString(m.render(e, false))
			}
			b.WriteString("\n  " + fg(cFaint).Render(strings.Repeat("┄", max(0, m.vp.Width()-4))) + "\n")
		}
		f.Unread = false
		empty = "no threads yet: type below to start one, @name to call someone"
	case a != nil:
		for _, e := range a.Log {
			b.WriteString(m.render(e, true))
		}
		a.Unread = false
		empty = a.Name + " has an empty desk"
	default:
		m.sel = "boss" // agent or forum vanished while we watched
	}
	if m.modal != nil && m.modal.a != nil && !o.Alive(m.modal.a) {
		m.modal = nil
	}
	o.Unlock()
	m.setContent(b.String(), empty, jump)
}

func (m *model) setContent(content, empty string, jump bool) {
	if content == "" {
		content = lipgloss.Place(m.vp.Width(), m.vp.Height(), lipgloss.Center, lipgloss.Center, dimSt.Render(empty))
	}
	m.plain = strings.Split(ansi.Strip(content), "\n")
	stick := jump || m.vp.AtBottom()
	m.vp.SetContent(content)
	if stick {
		m.vp.GotoBottom()
	}
}

func (m *model) openFile(p string) {
	m.sel, m.file = "files", p
	m.refresh(false)
	m.vp.GotoTop()
}

// filesView lists the output folder, or renders the open file: markdown as markdown, code highlighted.
func (m *model) filesView() string {
	ws := m.o.Files
	if m.file == "" {
		var b strings.Builder
		for _, p := range ws.Paths() {
			fmt.Fprintf(&b, "\n  📄 %s  %s", fg(cWhite).Render(p), dimSt.Render(fmt.Sprintf("%d lines", workspace.Lines(ws.Read(p)))))
		}
		if b.Len() == 0 {
			return ""
		}
		return b.String() + "\n"
	}
	key, ok := ws.ModKey(m.file)
	if !ok {
		return "\n  " + fg(cRed).Render(m.file+" is gone (moved or trashed)")
	}
	if key != m.fileKey {
		text := ws.Read(m.file)
		if ext := strings.TrimPrefix(filepath.Ext(m.file), "."); ext != "md" && ext != "markdown" {
			text = "```" + ext + "\n" + text + "\n```"
		}
		out, err := m.md.Render(text)
		if err != nil {
			out = ws.Read(m.file)
		}
		m.fileKey, m.fileCache = key, cached{out: out}
	}
	return m.fileCache.out
}

type tab struct {
	key, label string
	do         func()
}

// tabs renders the tab row and the underline row, registering click targets.
func (m *model) tabs() (string, string) {
	icon := func(working, unread bool) string {
		switch {
		case working:
			return m.sp.View() + " "
		case unread:
			return fg(cCyan).Render("●") + " "
		}
		return ""
	}
	var left, right []tab
	m.o.Do(func() {
		left = append(left, tab{"boss", icon(m.o.Boss.Working, m.o.Boss.Unread && m.sel != "boss") + "boss", nil})
		for _, a := range m.o.Agents {
			k := "a:" + a.Name
			left = append(left, tab{k, icon(a.Working, a.Unread && m.sel != k) + a.Name + " " + fg(scoreColor(a.Score)).Render(strconv.Itoa(a.Score)), nil})
		}
		right = append(right, tab{"all", "all", nil}, tab{"files", "files", nil})
		for _, f := range m.o.Forums {
			k, running := "f:"+f.Name, false
			for _, t := range f.Threads {
				running = running || t.Running
			}
			right = append(right, tab{k, icon(running, f.Unread && m.sel != k) + "#" + f.Name, nil})
		}
	})
	left = append(left, tab{"", "+", m.newAgent})
	right = append(right, tab{"", "+", m.newForum})

	var row, under strings.Builder
	x := 1 // outer padding
	draw := func(t tab) {
		st := tabSt
		if t.key == m.sel {
			st = tabOnSt
		}
		s := st.Render(t.label)
		w := lipgloss.Width(s)
		row.WriteString(s)
		if t.key == m.sel {
			under.WriteString(fg(cViolet).Render(strings.Repeat("━", w)))
		} else {
			under.WriteString(fg(cFaint).Render(strings.Repeat("─", w)))
		}
		do := t.do
		if do == nil {
			k := t.key
			do = func() {
				if a := m.agent(); a != nil && k == m.sel {
					m.openStats(a) // clicking the open agent tab pops out its stats
					return
				}
				if k == "files" {
					m.file = "" // the files tab always opens on the list
				}
				m.selectView(k)
			}
		}
		m.hits = append(m.hits, hit{tabsRow, x, x + w, do})
		x += w
	}
	for _, t := range left {
		draw(t)
	}
	rw := 0
	for _, t := range right {
		rw += lipgloss.Width(tabSt.Render(t.label))
	}
	gap := max(1, m.w-2-(x-1)-rw)
	row.WriteString(strings.Repeat(" ", gap))
	under.WriteString(fg(cFaint).Render(strings.Repeat("─", gap)))
	x += gap
	for _, t := range right {
		draw(t)
	}
	return row.String(), under.String()
}

// chips renders clickable buttons on a row starting at x.
func (m *model) chips(y, x int, items ...tab) string {
	var b strings.Builder
	for _, c := range items {
		s := chipSt.Render(c.label)
		w := lipgloss.Width(s)
		m.hits = append(m.hits, hit{y, x, x + w, c.do})
		b.WriteString(s)
		x += w
	}
	return b.String()
}

func (m *model) subBar() string {
	if a := m.agent(); a != nil {
		var info string
		m.o.Do(func() { info = fmt.Sprintf("%s · seed %d · %s", a.Role, a.Seed, a.Quirk) })
		return m.chips(subRow, 1,
			tab{label: "⚙ stats", do: func() { m.openStats(a) }},
			tab{label: "⌫ wipe desk", do: func() {
				m.confirm("Wipe "+a.Name+"'s desk?", "Clears everything on this desk. Score and traits stay.", func() { m.o.Do(func() { a.Log = nil }) })
			}},
			tab{label: "✕ delete", do: func() {
				m.confirm("Delete "+a.Name+"?", "They leave the office for good.", func() { m.o.Do(func() { m.o.Fire(a, "removed by you") }) })
			}}) + dimSt.Render("  "+info)
	}
	if f := m.forum(); f != nil {
		return m.chips(subRow, 1,
			tab{label: "✎ rename", do: func() {
				m.prompt("Rename #"+f.Name, f.Name, false, func(n string) {
					m.o.Do(func() {
						if m.o.RenameForum(f, n) {
							m.sel = "f:" + f.Name
						}
					})
				})
			}},
			tab{label: "⌫ wipe", do: func() {
				m.confirm("Wipe #"+f.Name+"?", "Deletes every thread in this forum.", func() { m.o.Do(func() { m.o.WipeForum(f) }) })
			}},
			tab{label: "✕ delete", do: func() {
				m.confirm("Delete #"+f.Name+"?", "Removes the forum and all its threads.", func() { m.o.Do(func() { m.o.DeleteForum(f) }) })
			}}) + dimSt.Render("  type to post · #3 … replies to thread 3 · @name calls someone")
	}
	switch m.sel {
	case "files":
		if m.file == "" {
			return dimSt.Render(" what the team made, in " + m.o.Files.Dir() + "/ · click a file to open it")
		}
		return m.chips(subRow, 1, tab{label: "← all files", do: func() { m.file = ""; m.refresh(true) }}) + fg(cWhite).Render("  "+m.file)
	case "boss":
		return m.chips(subRow, 1, tab{label: "⌫ wipe chat", do: func() {
			m.confirm("Wipe the boss chat?", "Clears your conversation and the boss's memory of it.", func() { m.o.Do(m.o.WipeChat) })
		}}) + dimSt.Render("  your conversation with the boss")
	}
	return dimSt.Render(" everything happening in the office")
}

func (m *model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "office"
	if m.w == 0 {
		return v
	}
	m.hits = m.hits[:0]
	s := m.o.Settings.Get()
	where := s.Connector
	if s.Model() != "" {
		where += " · " + s.Model()
	}
	left := logoSt.Render("office")
	settings := chipSt.Render("⚙ settings")
	right := dimSt.Render(where) + " " + settings
	pad := max(1, m.w-2-lipgloss.Width(left)-lipgloss.Width(right))
	sx := 1 + lipgloss.Width(left) + pad + lipgloss.Width(dimSt.Render(where)) + 1
	m.hits = append(m.hits, hit{headerRow, sx, sx + lipgloss.Width(settings), m.openSettings})
	header := left + strings.Repeat(" ", pad) + right
	tabRow, underline := m.tabs()

	main := m.vp.View()
	if !m.ready {
		main = lipgloss.Place(m.vp.Width(), m.vp.Height(), lipgloss.Center, lipgloss.Center, m.setupCard(where))
	}
	switch {
	case m.forum() != nil:
		m.in.Placeholder = "Post in #" + strings.TrimPrefix(m.sel, "f:") + "…"
	case m.busy:
		m.in.Placeholder = "The boss is thinking…"
	default:
		m.in.Placeholder = "Message the boss… (/exam for a pop quiz)"
	}
	help := "enter send · tab or click to switch · ctrl+e stats · ctrl+o settings · pgup/pgdn scroll · ctrl+c quit"
	if m.err != "" && m.ready {
		help = fg(cRed).Render(m.err)
	}
	screen := lipgloss.NewStyle().Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Left,
		header, "", tabRow, underline, m.subBar(), main, inputSt.Width(m.w-2).Render(m.in.View()), dimSt.Render(help)))

	if m.modal != nil {
		m.rows = map[int]func(int){}
		pop := m.modalView()
		x := max(0, (m.w-lipgloss.Width(pop))/2)
		y := max(0, (m.h-lipgloss.Height(pop))/2)
		w, h := lipgloss.Width(pop), lipgloss.Height(pop)
		m.pop = [4]int{x, y, w, h}
		// ASCII x inside the top padding: wide glyphs like ✕ render 2 cells in some terminals and shift the border
		closeX := fg(cMuted).Bold(true).Render("x")
		screen = lipgloss.NewCompositor(lipgloss.NewLayer(screen), lipgloss.NewLayer(pop).X(x).Y(y).Z(1),
			lipgloss.NewLayer(closeX).X(x+w-4).Y(y+1).Z(2)).Render()
	}
	v.SetContent(screen)
	return v
}

// setupCard shows connector setup: download progress, or the error with a way out.
func (m *model) setupCard(where string) string {
	status := m.setup.status
	if status == "" {
		status = "connecting…"
	}
	pct := 0.0
	if m.setup.total > 0 {
		pct = m.setup.done / m.setup.total
		status += fmt.Sprintf("  %.2f / %.2f GB", m.setup.done/1e9, m.setup.total/1e9)
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Render("Setting up the office"), dimSt.Render(where), "", m.bar.ViewAs(pct), "", dimSt.Render(status)}
	if m.err != "" {
		lines = append(lines[:3], fg(cRed).Width(60).Render(m.err), "", dimSt.Render("open ⚙ settings (ctrl+o) to pick another connector or model"))
	}
	return cardSt.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}
