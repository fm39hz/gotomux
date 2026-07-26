package picker

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// TeaOpts: force /dev/tty so display-popup still gets a real TTY.
// Inline always (fzf-style) — alt-screen inside tmux causes scrollback artifacts
// because tmux intercepts terminal escape codes. ClearInline handles cleanup.
//
// WithoutSignalHandler: main owns SIGINT for the picker phase only.
//   - raw TTY: Ctrl+C arrives as KeyMsg -> ActionQuit (cancel)
//   - SIGINT (non-raw / spam): main calls Program.Quit() -> cancel, exit 0
func TeaOpts() (opts []tea.ProgramOption, alt bool, err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return []tea.ProgramOption{tea.WithoutSignalHandler()}, false, nil
	}
	return []tea.ProgramOption{
		tea.WithInput(tty),
		tea.WithOutput(tty),
		tea.WithoutSignalHandler(),
	}, false, nil
}

// truncateRunes cuts s to at most n runes, adding "..." when clipped.
// Ellipsis is 3 runes; for n < 3 use "." * n so the result never exceeds n.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 3 {
		return strings.Repeat(".", n)
	}
	return string(r[:n-3]) + "..."
}

// ClearInline erases n lines of residual bubbletea inline UI (fzf-style).
// Bubble Tea stop() only clears the current line - the rest stays in scrollback.
func ClearInline(n int) {
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
	// prefer /dev/tty - same surface as WithOutput(tty)
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		fmt.Fprint(tty, b.String())
		tty.Close()
		return
	}
	fmt.Fprint(os.Stdout, b.String())
}
