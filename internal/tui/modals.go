package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mikolajwichrowski/baitai/internal/config"
	"github.com/mikolajwichrowski/baitai/internal/llm"
	"github.com/mikolajwichrowski/baitai/internal/office"
)

// modal is the popup layer: agent stats editor, confirmation, text prompt or settings.
type modal struct {
	kind    string // "stats", "confirm", "prompt", "settings"
	title   string
	body    string
	a       *office.Agent
	score   int // draft score, applied on close
	field   int
	editing bool
	input   textinput.Model
	onYes   func()
	onText  func(string)
}

// option is one settings row: a value you adjust with ‹ ›, and/or an action on enter.
type option struct {
	label  string
	value  func() string
	adjust func(d int)
	act    func()
}

func (m *model) confirm(title, body string, yes func()) {
	m.modal = &modal{kind: "confirm", title: title, body: body, onYes: yes}
}

func (m *model) prompt(title, value string, secret bool, done func(string)) {
	in := newInput("› ", "")
	if secret {
		in.EchoMode = textinput.EchoPassword
	}
	in.SetValue(value)
	in.SetWidth(44)
	m.modal = &modal{kind: "prompt", title: title, input: in, onText: done}
}

func (m *model) openStats(a *office.Agent) {
	m.o.Do(func() { m.modal = &modal{kind: "stats", a: a, score: a.Score} })
}

func (m *model) openSettings() { m.modal = &modal{kind: "settings", title: "Settings"} }

func (m *model) newAgent() {
	m.prompt("Hire an agent: role?", "", false, func(role string) {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" {
			return
		}
		var a *office.Agent
		m.o.Do(func() {
			if len(m.o.Agents) < m.o.Settings.Get().MaxAgents {
				a = m.o.Hire(role)
				m.o.Note(fmt.Sprintf("🤝 You hired **%s** as %s (seed %d)", a.Name, a.Role, a.Seed))
			}
		})
		if a != nil {
			m.selectView("a:" + a.Name)
			m.openStats(a)
		}
	})
}

func (m *model) newForum() {
	m.prompt("New forum name", "", false, func(name string) {
		var f *office.Forum
		m.o.Do(func() { f = m.o.NewForum(name) })
		if f != nil {
			m.selectView("f:" + f.Name)
		}
	})
}

// ---------- settings ----------

func (m *model) set(change func(*config.Settings)) { m.o.Settings.Update(change) }

// settingsOptions depend on the connector, so they're rebuilt on every draw.
func (m *model) settingsOptions() []option {
	s := m.o.Settings.Get()
	conn := s.Connector
	num := func(label string, field func(*config.Settings) *int, lo, hi, step int) option {
		return option{label: label,
			value:  func() string { s := m.o.Settings.Get(); return strconv.Itoa(*field(&s)) },
			adjust: func(d int) { m.set(func(s *config.Settings) { *field(s) = min(max(*field(s)+d*step, lo), hi) }) }}
	}
	text := func(label string, get func(config.Settings) string, put func(*config.Settings, string), secret bool, hint func() string) option {
		return option{label: label, value: func() string { return get(m.o.Settings.Get()) }, act: func() {
			current := get(m.o.Settings.Get())
			if secret {
				current = ""
			}
			m.prompt(label, current, secret, func(v string) {
				m.set(func(s *config.Settings) { put(s, strings.TrimSpace(v)) })
				m.reconnect()
			})
			if hint != nil {
				m.modal.body = hint()
			}
		}}
	}
	opts := []option{{label: "Connector", value: func() string { return m.o.Settings.Get().Connector }, adjust: func(d int) {
		m.set(func(s *config.Settings) {
			i := slices.Index(config.Connectors, s.Connector)
			s.Connector = config.Connectors[(i+d+len(config.Connectors))%len(config.Connectors)]
		})
		m.reconnect()
	}}}
	opts = append(opts, text("Model",
		func(s config.Settings) string { return orDefault(s.Model(), "the tool's default") },
		func(s *config.Settings, v string) { s.Models[s.Connector] = v }, false,
		func() string {
			if o, ok := m.o.Provider().(*llm.Ollama); ok {
				if names := o.Installed(); len(names) > 0 {
					return "installed: " + strings.Join(names, ", ") + "\nany other Ollama model is downloaded on first use"
				}
			}
			return "empty = the connector's default"
		}))
	if conn != config.ClaudeCLI {
		opts = append(opts, text("Endpoint",
			func(s config.Settings) string { return orDefault(s.BaseURL(), "default") },
			func(s *config.Settings, v string) { s.BaseURLs[s.Connector] = v }, false, nil))
	}
	if conn == config.OpenAI || conn == config.Anthropic || conn == config.OpenAICompatible {
		env := map[string]string{config.OpenAI: "$OPENAI_API_KEY", config.Anthropic: "$ANTHROPIC_API_KEY / ant login"}[conn]
		opts = append(opts, text("API key",
			func(s config.Settings) string { return mask(s.APIKey(), env) },
			func(s *config.Settings, v string) { s.APIKeys[s.Connector] = v }, true,
			func() string {
				return "stored in settings.json (owner-only); leave empty to use " + orDefault(env, "none")
			}))
	}
	opts = append(opts, option{label: "Agent thinking", value: func() string { return onOff(m.o.Settings.Get().Thinking) },
		adjust: func(int) { m.set(func(s *config.Settings) { s.Thinking = !s.Thinking }) }})
	if conn == config.Ollama {
		opts = append(opts, num("Context window (tokens)", func(s *config.Settings) *int { return &s.Context }, 2048, 131072, 2048))
	}
	opts = append(opts,
		num("Max agents", func(s *config.Settings) *int { return &s.MaxAgents }, 1, 12, 1),
		num("Forum rounds per task", func(s *config.Settings) *int { return &s.Rounds }, 1, 10, 1),
		num("Boss send-backs per task", func(s *config.Settings) *int { return &s.FollowUps }, 0, 5, 1),
		option{label: "Restart office…", act: func() {
			m.confirm("Restart office?", "All agents, chats and forums are wiped and a fresh team is hired.\nFiles in the output folder are kept.", func() {
				m.o.Do(m.o.Reset)
				clear(m.cache)
				m.sel = "boss"
			})
		}},
		option{label: "Forget legends…", value: func() (v string) {
			m.o.Do(func() { v = strconv.Itoa(len(m.o.Legends)) + " saved" })
			return
		}, act: func() {
			m.confirm("Forget legends?", "Future hires will no longer take after past legends.", func() {
				m.o.Do(func() { m.o.Legends = nil; m.o.SaveLegends() })
			})
		}})
	return opts
}

var connectorNotes = map[string]string{
	config.Ollama:           "runs models on this machine",
	config.Anthropic:        "Claude API; refusals fall back to another model automatically",
	config.OpenAI:           "OpenAI API",
	config.OpenAICompatible: "any Chat Completions endpoint: LM Studio, OpenRouter, Groq, vLLM, Gemini…",
	config.ClaudeCLI:        "uses your Claude Code login with every tool switched off",
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func mask(key, env string) string {
	if key == "" {
		return orDefault("from "+env, "not set")
	}
	return "••••" + key[max(0, len(key)-4):]
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// ---------- stats ----------

// statsText are the editable text fields after score and traits.
var statsText = []string{"name", "seed", "quirk", "role", "prompt"}

func (m *model) statsKey(k string) {
	md, a := m.modal, m.modal.a
	traits := office.Traits
	nFields := 1 + len(traits) + len(statsText)
	if md.editing {
		switch k {
		case "enter":
			v := strings.TrimSpace(md.input.Value())
			m.o.Do(func() {
				switch statsText[md.field-1-len(traits)] {
				case "name":
					if old := a.Name; m.o.Rename(a, v) && m.sel == "a:"+old {
						m.sel = "a:" + a.Name
					}
				case "seed":
					if n, err := strconv.ParseInt(v, 10, 64); err == nil {
						office.Reseed(a, n)
					}
				case "quirk":
					a.Quirk = v
				case "role":
					if v != "" {
						a.Role = strings.ToLower(v)
					}
				case "prompt":
					a.Base = v
				}
			})
			md.editing = false
		case "esc":
			md.editing = false
		}
		return
	}
	adjust := func(d int) {
		if md.field == 0 {
			md.score = min(max(md.score+d, 0), office.MaxScore)
		} else if md.field <= len(traits) {
			t := traits[md.field-1]
			m.o.Do(func() { a.Stats[t] = min(max(a.Stats[t]+d, 1), 10) })
		}
	}
	switch k {
	case "up", "k":
		md.field = (md.field + nFields - 1) % nFields
	case "down", "j":
		md.field = (md.field + 1) % nFields
	case "left", "h", "-":
		adjust(-1)
	case "right", "l", "+", "=":
		adjust(1)
	case "enter":
		if md.field > len(traits) {
			var v string
			m.o.Do(func() {
				v = map[string]string{"name": a.Name, "seed": strconv.FormatInt(a.Seed, 10), "quirk": a.Quirk, "role": a.Role, "prompt": a.Base}[statsText[md.field-1-len(traits)]]
			})
			md.input = newInput("", "")
			md.input.SetWidth(44)
			md.input.SetValue(v)
			md.editing = true
		}
	case "esc", "ctrl+e":
		m.closeModal()
	}
}

// closeModal closes the popup the same way esc does; the stats editor commits its draft score.
func (m *model) closeModal() {
	if md := m.modal; md != nil && md.kind == "stats" {
		m.o.Do(func() {
			if m.o.Alive(md.a) {
				m.o.Rescore(md.a, md.score-md.a.Score, "adjusted by the user")
			}
		})
	}
	m.modal = nil
}

func (m *model) modalKey(e tea.KeyPressMsg) tea.Cmd {
	md := m.modal
	k := e.String()
	var cmd tea.Cmd
	switch md.kind {
	case "stats":
		wasEditing := md.editing
		m.statsKey(k)
		if wasEditing && md.editing {
			md.input, cmd = md.input.Update(e)
		}
	case "confirm":
		switch k {
		case "y", "enter":
			m.modal = nil
			md.onYes()
		case "n", "esc":
			m.modal = nil
		}
	case "prompt":
		switch k {
		case "enter":
			m.modal = nil
			md.onText(md.input.Value())
		case "esc":
			m.modal = nil
		default:
			md.input, cmd = md.input.Update(e)
		}
	case "settings":
		opts := m.settingsOptions()
		md.field = min(md.field, len(opts)-1)
		o := opts[md.field]
		switch k {
		case "up", "k":
			md.field = (md.field + len(opts) - 1) % len(opts)
		case "down", "j":
			md.field = (md.field + 1) % len(opts)
		case "left", "h", "-":
			if o.adjust != nil {
				o.adjust(-1)
			}
		case "right", "l", "+", "=":
			if o.adjust != nil {
				o.adjust(1)
			}
		case "enter":
			if o.act != nil {
				o.act()
			}
		case "esc", "ctrl+o":
			m.modal = nil
		}
	}
	m.refresh(false)
	return cmd
}

func (m *model) statsModal() string {
	md, a := m.modal, m.modal.a
	traits := office.Traits
	var lines []string
	m.o.Do(func() {
		title := pillSt.Background(lipgloss.Color(a.Col)).Render(a.Name) + "  " + fg(cWhite).Bold(true).Render(a.Role)
		if a.Legend {
			title += "  🏆 legend"
		}
		lines = append(lines, title, "")
		row := func(i int, label, value string) {
			m.rows[len(lines)] = func(dx int) {
				md.editing = false
				md.field = i
				if v := dx - 12; i <= len(traits) && v >= 1 && v <= 10 { // clicked a bar cell
					if i == 0 {
						md.score = v
					} else {
						m.o.Do(func() { a.Stats[traits[i-1]] = v })
					}
				} else if i > len(traits) {
					m.statsKey("enter") // text field: start editing
				}
			}
			mark, st := "  ", dimSt
			if i == md.field {
				mark, st = fg(cViolet).Render("› "), fg(cWhite).Bold(true)
			}
			lines = append(lines, mark+st.Render(fmt.Sprintf("%-10s", label))+" "+value)
		}
		row(0, "score", bar(md.score, office.MaxScore, scoreColor(md.score))+fmt.Sprintf("  %d/10", md.score))
		for i, t := range traits {
			row(i+1, t, bar(a.Stats[t], 10, a.Col)+fmt.Sprintf("  %d", a.Stats[t]))
		}
		lines = append(lines, "")
		vals := map[string]string{"name": a.Name + dimSt.Render("  (letters, digits, - _)"), "seed": strconv.FormatInt(a.Seed, 10) + dimSt.Render("  (sets quirk & colour)"), "quirk": a.Quirk, "role": a.Role, "prompt": a.Base}
		for i, k := range statsText {
			f := 1 + len(traits) + i
			v := vals[k]
			if md.editing && md.field == f {
				v = md.input.View()
			} else if k == "prompt" {
				v = lipgloss.NewStyle().Width(44).Render(v)
			}
			row(f, k, v)
		}
		if n := len(a.History); n > 0 {
			lines = append(lines, "")
			for _, h := range a.History[max(0, n-3):] {
				lines = append(lines, dimSt.Render("  "+h))
			}
		}
	})
	help := "↑↓ select · ←→ adjust · enter edit · esc close"
	if md.editing {
		help = "enter save · esc cancel"
	}
	return modalSt.Width(64).Render(strings.Join(lines, "\n") + "\n\n" + dimSt.Render(help))
}

func (m *model) modalView() string {
	md := m.modal
	title := fg(cWhite).Bold(true).Render(md.title)
	switch md.kind {
	case "stats":
		return m.statsModal()
	case "confirm":
		m.rows[3+strings.Count(md.body, "\n")+1] = func(dx int) {
			m.modal = nil
			if dx < 8 {
				md.onYes()
			}
		}
		return modalSt.Width(56).Render(title + "\n\n" + md.body + "\n\n" +
			pillSt.Background(lipgloss.Color(cRed)).Render("y  yes") + "  " + dimSt.Render("n / esc  cancel"))
	case "prompt":
		body := ""
		if md.body != "" {
			body = "\n\n" + dimSt.Width(52).Render(md.body)
		}
		return modalSt.Width(60).Render(title + "\n\n" + md.input.View() + body + "\n\n" + dimSt.Render("enter ok · esc cancel"))
	}
	opts := m.settingsOptions()
	var lines []string
	for i, o := range opts {
		m.rows[2+i] = func(dx int) {
			md.field = i
			switch {
			case o.adjust != nil && dx >= 28 && dx < 30: // ‹
				o.adjust(-1)
			case o.adjust != nil && dx >= 30: // value or ›
				o.adjust(1)
			case o.act != nil:
				o.act()
			}
		}
		mark, st := "  ", dimSt
		if i == md.field {
			mark, st = fg(cViolet).Render("› "), fg(cWhite).Bold(true)
		}
		v := ""
		switch {
		case o.adjust != nil:
			v = fg(cViolet).Render("‹ ") + o.value() + fg(cViolet).Render(" ›")
		case o.value != nil:
			v = fg(cCyan).Render(truncate(o.value(), 38))
		}
		lines = append(lines, mark+st.Render(fmt.Sprintf("%-26s", o.label))+v)
	}
	conn := m.o.Settings.Get().Connector
	info := dimSt.Render(fmt.Sprintf("%s: %s\nstate   %s\noutput  %s (agents' files; trash in .trash)", conn, connectorNotes[conn], m.o.Dir, m.o.Files.Dir()))
	return modalSt.Width(72).Render(title + "\n\n" + strings.Join(lines, "\n") + "\n\n" + info + "\n\n" + dimSt.Render("↑↓ select · ←→ or click ‹ › adjust · enter choose · esc close"))
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
