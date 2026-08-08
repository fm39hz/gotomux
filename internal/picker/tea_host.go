package picker

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// TeaOpts: force /dev/tty so display-popup still gets a real TTY.
// Inline (fzf-style). Bubble Tea owns its frame cleanup; the host shell owns its
// prompt and mode repaint. Trying to erase a fixed number of rows here corrupts
// output above multi-line prompts because their geometry is shell-specific.
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
