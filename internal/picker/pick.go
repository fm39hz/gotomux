package picker

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type pickModel struct {
	all        []string
	view       []string
	cursor     int
	queryInput textinput.Model
	name       string
	quit       bool
}

func Pick(names []string) (string, error) {
	if len(names) == 1 {
		return names[0], nil
	}
	m := pickModel{all: names, queryInput: initInput()}
	m.refilter()
	p := tea.NewProgram(m, tea.WithoutSignalHandler())
	final, err := RunCancellable(p)
	if err != nil {
		return "", fmt.Errorf("pick: %w", err)
	}
	pm, ok := final.(pickModel)
	if !ok {
		return "", fmt.Errorf("pick: unexpected model type %T", final)
	}
	return pm.name, nil
}

func (m *pickModel) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.queryInput.Value()))
	m.view = m.view[:0]
	for _, n := range m.all {
		if q == "" || fuzzyMatch(q, strings.ToLower(n)) {
			m.view = append(m.view, n)
		}
	}
	if m.cursor >= len(m.view) {
		m.cursor = len(m.view) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m pickModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, pickKeys.Quit):
			m.quit = true
			return m, tea.Quit
		case key.Matches(msg, pickKeys.Confirm):
			if len(m.view) > 0 {
				m.name = m.view[m.cursor]
				return m, tea.Quit
			}
			return m, nil
		case key.Matches(msg, pickKeys.Up):
			if len(m.view) > 0 {
				m.cursor--
				if m.cursor < 0 {
					m.cursor = len(m.view) - 1
				}
			}
			return m, nil
		case key.Matches(msg, pickKeys.Down):
			if len(m.view) > 0 {
				m.cursor = (m.cursor + 1) % len(m.view)
			}
			return m, nil
		}

		// Unhandled key: pass to textinput
		if msg.Key().Mod != 0 && msg.Key().Mod != tea.ModShift {
			return m, nil
		}
	}

	prev := m.queryInput.Value()
	var cmd tea.Cmd
	m.queryInput, cmd = m.queryInput.Update(msg)
	if m.queryInput.Value() != prev {
		m.refilter()
	}
	return m, cmd
}

func (m pickModel) View() tea.View {
	var b strings.Builder
	b.WriteString(m.queryInput.View())
	b.WriteString("\n")
	b.WriteString(styleHeader.Render("  freeze session  ^n/p | enter | esc"))
	b.WriteString("\n")
	for i, n := range m.view {
		line := n
		if i == m.cursor {
			b.WriteString(styleCursor.Render("> " + line))
		} else {
			b.WriteString(stylePreset.Render("  " + line))
		}
		b.WriteByte('\n')
	}
	return tea.NewView(b.String())
}
