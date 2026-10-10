// Package tui is the terminal interface: tabs for the boss, every agent's desk, the whole office,
// the output folder and the forums, with popups for stats and settings.
package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/mikolajwichrowski/baitai/internal/llm"
	"github.com/mikolajwichrowski/baitai/internal/office"
)

type (
	progressMsg struct {
		status      string
		done, total float64
	}
	readyMsg struct{ err error }
	doneMsg  struct{ err error }
	saveMsg  struct{}
)

// Screen rows: header, blank, tabs, underline, sub-bar, then the main view.
const (
	headerRow = 0
	tabsRow   = 2
	subRow    = 4
	chrome    = 9 // rows not used by the main view
)

type hit struct {
	y, x0, x1 int
	do        func()
}

type cached struct {
	n   int
	out string
}

type model struct {
	o           *office.Office
	prog        *tea.Program
	vp          viewport.Model
	in          textinput.Model
	sp          spinner.Model
	bar         progress.Model
	md          *glamour.TermRenderer
	sel         string // "boss", "all", "files", "a:<agent>" or "f:<forum>"
	file        string // file open in the files view, "" for the list
	fileCache   cached
	fileKey     string
	plain       []string // the main view's lines without styling, to find clicked file names
	hits        []hit
	cache       map[*office.Entry]cached
	modal       *modal
	busy, ready bool
	setup       progressMsg
	err         string
	w, h        int
	pop         [4]int               // popup x, y, width, height for click handling
	rows        map[int]func(dx int) // popup content line → click handler
}

// Run starts the interface and blocks until you quit; the office is saved on the way out.
func Run(o *office.Office) error {
	m := &model{
		o: o, in: newInput("❯ ", ""), sel: "boss", cache: map[*office.Entry]cached{},
		sp:  spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(fg(cAmber))),
		bar: progress.New(progress.WithDefaultBlend(), progress.WithWidth(48)),
	}
	m.prog = tea.NewProgram(m)
	_, err := m.prog.Run()
	if serr := o.Save(); err == nil {
		err = serr
	}
	return err
}

func newInput(prompt, placeholder string) textinput.Model {
	in := textinput.New()
	in.Prompt = prompt
	in.Placeholder = placeholder
	s := in.Styles()
	s.Focused.Prompt = s.Focused.Prompt.Foreground(lipgloss.Color(cViolet))
	in.SetStyles(s)
	in.Focus()
	return in
}

func saveTick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return saveMsg{} }) }

func (m *model) Init() tea.Cmd { return tea.Batch(m.sp.Tick, m.prepare(), saveTick()) }

// prepare gets the connector ready (Ollama starts and pulls the model) and reports progress.
func (m *model) prepare() tea.Cmd {
	m.ready, m.setup, m.err = false, progressMsg{}, ""
	p, ok := m.o.Provider().(llm.Preparer)
	if !ok {
		return func() tea.Msg { return readyMsg{} }
	}
	return func() tea.Msg {
		return readyMsg{p.Prepare(context.Background(), func(status string, done, total float64) {
			m.prog.Send(progressMsg{status, done, total})
		})}
	}
}

// reconnect applies changed connector settings.
func (m *model) reconnect() {
	if err := m.o.UseSettings(); err != nil {
		m.err = err.Error()
		return
	}
	cmd := m.prepare()
	go func() { m.prog.Send(cmd()) }()
}

// agent and forum return the selected one, if any; they take the lock themselves.
func (m *model) agent() (a *office.Agent) {
	if name, ok := strings.CutPrefix(m.sel, "a:"); ok {
		m.o.Do(func() {
			for _, x := range m.o.Agents {
				if x.Name == name {
					a = x
				}
			}
		})
	}
	return a
}

func (m *model) forum() (f *office.Forum) {
	if name, ok := strings.CutPrefix(m.sel, "f:"); ok {
		m.o.Do(func() { f = m.o.ForumByName(name) })
	}
	return f
}

func (m *model) keys() []string {
	keys := []string{"boss"}
	m.o.Do(func() {
		for _, a := range m.o.Agents {
			keys = append(keys, "a:"+a.Name)
		}
		keys = append(keys, "all", "files")
		for _, f := range m.o.Forums {
			keys = append(keys, "f:"+f.Name)
		}
	})
	return keys
}

func (m *model) selectView(k string) {
	m.sel = k
	m.refresh(true)
}

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch e := message.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = e.Width, e.Height
		m.vp = viewport.New(viewport.WithWidth(m.w-2), viewport.WithHeight(m.h-chrome))
		m.in.SetWidth(m.w - 10)
		m.md, _ = glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(m.w-8), glamour.WithPreservedNewLines())
		clear(m.cache)
		m.fileKey = ""
		m.refresh(true)
	case tea.KeyPressMsg:
		if e.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.modal != nil {
			return m, m.modalKey(e)
		}
		switch e.String() {
		case "tab", "shift+tab":
			keys := m.keys()
			i := 0
			for j, k := range keys {
				if k == m.sel {
					i = j
				}
			}
			if e.String() == "tab" {
				i++
			} else {
				i += len(keys) - 1
			}
			m.selectView(keys[i%len(keys)])
			return m, nil
		case "ctrl+e":
			if a := m.agent(); a != nil {
				m.openStats(a)
			}
			return m, nil
		case "ctrl+o":
			m.openSettings()
			return m, nil
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(e)
			return m, cmd
		case "enter":
			return m, m.send()
		}
	case tea.MouseClickMsg:
		m.click(e.Mouse())
		return m, nil
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(e)
		return m, cmd
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(e)
		m.refresh(false) // the team works in the background, so redraw at spinner rate
		return m, cmd
	case saveMsg:
		if err := m.o.Save(); err != nil {
			m.err = "save failed: " + err.Error()
		}
		return m, saveTick()
	case progressMsg:
		m.setup = e
	case readyMsg:
		if e.err != nil {
			m.err = e.err.Error()
			break
		}
		m.ready = true
		m.o.Do(func() {
			if len(m.o.Boss.Log) == 0 {
				m.o.Say(office.KindBoss, "Office is open. Tell me what you need, and who to **hire** or **fire**. Click a tab to watch any desk, a forum, or **all** of it.")
			}
		})
		m.refresh(true)
	case doneMsg:
		m.busy = false
		if e.err != nil {
			m.o.Do(func() { m.o.Say(office.KindError, e.err.Error()) })
		}
		m.refresh(false)
	}
	if m.modal != nil {
		return m, nil
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(message)
	return m, cmd
}

// send delivers the input: to the forum you're looking at, otherwise to the boss.
func (m *model) send() tea.Cmd {
	q := strings.TrimSpace(m.in.Value())
	if q == "" || !m.ready {
		return nil
	}
	if q == "/exam" { // pop quiz for the whole team, graded against a secret key
		m.in.Reset()
		go m.o.Exam()
		return nil
	}
	if f := m.forum(); f != nil {
		m.in.Reset()
		m.o.UserPost(f, q)
		m.refresh(true)
		return nil
	}
	if m.busy {
		return nil
	}
	m.in.Reset()
	m.busy = true
	if m.sel != "all" {
		m.sel = "boss"
	}
	m.refresh(true)
	return func() tea.Msg { return doneMsg{m.o.Ask(q)} }
}

// click handles mouse clicks: popup rows, tabs and chips, then file names in the main view.
func (m *model) click(mo tea.Mouse) {
	defer m.refresh(false)
	if m.modal != nil {
		x, y, w, h := m.pop[0], m.pop[1], m.pop[2], m.pop[3]
		inside := mo.X >= x && mo.X < x+w && mo.Y >= y && mo.Y < y+h
		switch {
		case !inside || mo.Y <= y+1 && mo.X >= x+w-6: // outside or on the x
			m.closeModal()
		case m.rows[mo.Y-y-2] != nil: // content starts after border + padding
			m.rows[mo.Y-y-2](mo.X - x - 3)
		}
		return
	}
	for _, h := range m.hits {
		if mo.Y == h.y && mo.X >= h.x0 && mo.X < h.x1 {
			h.do()
			return
		}
	}
	if line := mo.Y - subRow - 1 + m.vp.YOffset(); mo.Y > subRow && line < len(m.plain) {
		if p := m.o.Files.PathAt(m.plain[line], mo.X-1); p != "" {
			m.openFile(p)
		}
	}
}
