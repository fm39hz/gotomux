package picker

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type pickModel struct {
	all    []string
	view   []string
	cursor int
	query  string
	name   string
	quit   bool
}

func Pick(names []string) (string, error) {
	if len(names) == 1 {
		return names[0], nil
	}
	m := pickModel{all: names}
	m.refilter()

	// Own SIGINT for pick lifetime — same cancel intent as esc/ctrl+c KeyMsg.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	p := tea.NewProgram(m, tea.WithoutSignalHandler())
	sigDone := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			p.Quit()
		case <-sigDone:
		}
	}()
	final, err := p.Run()
	close(sigDone)
	signal.Stop(sigCh)
	// drain pending SIGINT so disposition is clean for caller
	for {
		select {
		case <-sigCh:
			continue
		default:
		}
		break
	}

	if err != nil && err != tea.ErrInterrupted {
		return "", fmt.Errorf("pick: %w", err)
	}
	if err == tea.ErrInterrupted {
		return "", nil
	}
	pm, ok := final.(pickModel)
	if !ok {
		return "", fmt.Errorf("pick: unexpected model type %T", final)
	}
	ClearInline(2 + len(pm.view))
	return pm.name, nil
}

func (m *pickModel) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.query))
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

func (m pickModel) Init() tea.Cmd { return nil }

func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		case "enter":
			if len(m.view) > 0 {
				m.name = m.view[m.cursor]
				return m, tea.Quit
			}
		case "ctrl+n", "down":
			if len(m.view) > 0 {
				m.cursor = (m.cursor + 1) % len(m.view)
			}
		case "ctrl+p", "up":
			if len(m.view) > 0 {
				m.cursor--
				if m.cursor < 0 {
					m.cursor = len(m.view) - 1
				}
			}
		case "backspace":
			if len(m.query) > 0 {
				r := []rune(m.query)
				m.query = string(r[:len(r)-1])
				m.refilter()
			}
		default:
			if isModifierChord(msg) {
				return m, nil
			}
			if msg.Type == tea.KeyRunes {
				for _, r := range msg.Runes {
					if unicode.IsPrint(r) {
						m.query += string(r)
					}
				}
				m.refilter()
			}
		}
	}
	return m, nil
}

func (m pickModel) View() string {
	var b strings.Builder
	b.WriteString(m.query)
	b.WriteString("\n")
	b.WriteString(styleHeader.Render("  freeze session  ctrl-n/p · enter · esc"))
	b.WriteString("\n")
	for i, n := range m.view {
		line := n
		if i == m.cursor {
			b.WriteString(styleCursor.Render("▸ " + line))
		} else {
			b.WriteString(stylePreset.Render("  " + line))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
