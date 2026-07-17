package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// teaOpts: force /dev/tty so display-popup + default-shell=nu still get a real TTY.
// Inside tmux (incl. popups) use alt-screen — more reliable than inline redraw there.
func teaOpts() (opts []tea.ProgramOption, alt bool, err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// fall back to defaults (stdin/stdout) — works in a normal terminal
		if os.Getenv("TMUX") != "" {
			return []tea.ProgramOption{tea.WithAltScreen()}, true, nil
		}
		return nil, false, nil
	}
	opts = []tea.ProgramOption{
		tea.WithInput(tty),
		tea.WithOutput(tty),
	}
	if os.Getenv("TMUX") != "" {
		opts = append(opts, tea.WithAltScreen())
		return opts, true, nil
	}
	return opts, false, nil
}

// truncateRunes cuts s to at most n runes, adding "…" when clipped.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// isModifierChord: ctrl/alt/meta combo that is not plain text.
// Prevents ctrl+l etc. from inserting "l" into the filter.
func isModifierChord(msg tea.KeyMsg) bool {
	if msg.Alt {
		return true
	}
	s := msg.String()
	if strings.HasPrefix(s, "ctrl+") || strings.HasPrefix(s, "alt+") ||
		strings.HasPrefix(s, "shift+ctrl+") || strings.HasPrefix(s, "ctrl+alt+") {
		return true
	}
	if strings.Contains(s, "+") && msg.Type != tea.KeyRunes {
		return true
	}
	return false
}

// clearInline erases n lines of residual bubbletea inline UI (fzf-style).
// Bubble Tea stop() only clears the current line — the rest stays in scrollback.
func clearInline(n int) {
	if n <= 0 {
		return
	}
	var b strings.Builder
	// cursor is at start of last rendered line after stop(); go up n-1 then erase n
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString("\x1b[1A") // up
		}
		b.WriteString("\x1b[2K") // erase line
	}
	b.WriteByte('\r')
	fmt.Fprint(os.Stdout, b.String())
}
