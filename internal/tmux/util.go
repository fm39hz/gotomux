package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EscapeTmuxSeparator returns str unchanged unless it is ";", which is a tmux
// command separator. A lone ";" argument would be interpreted as a separator,
// breaking the command chain. Escaped to "\;" to suppress this.
func EscapeTmuxSeparator(str string) string {
	if str == ";" {
		return `\;`
	}
	return str
}

// EscapeTmuxTitle escapes a string for use as a tmux pane title. tmux's
// select-pane -T expands format expressions denoted by '#', so each '#' is
// doubled to '##'. Also escapes lone ";" to prevent separator injection.
func EscapeTmuxTitle(str string) string {
	return EscapeTmuxSeparator(strings.ReplaceAll(str, "#", "##"))
}

// EscapeSingleQuote escapes a string for safe use inside a single-quoted shell
// argument. Any single-quote character "'" is replaced with the sequence
// "'\\''", and the whole string is wrapped in single quotes.
func EscapeSingleQuote(str string) string {
	return "'" + strings.ReplaceAll(str, "'", "'\\''") + "'"
}

// TmuxFloatingPaneInfo attempts to detect whether the current tmux session
// supports floating panes (tmux >= 3.7). It checks TMUX_PANE and runs a
// diagnostic tmux command. Returns window dimensions and true if supported.
func TmuxFloatingPaneInfo() (width, height int, ok bool) {
	target := os.Getenv("TMUX_PANE")
	if target == "" {
		return 0, 0, false
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", target,
		"#{window_width} #{window_height}", ";", "list-commands", "new-pane").Output()
	if err != nil || !strings.Contains(string(out), "new-pane") {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(string(out), "%d %d", &width, &height); err != nil {
		return 0, 0, false
	}
	if width < 3 || height < 3 {
		return 0, 0, false
	}
	return width, height, true
}

// WindowPosition represents a position for tmux popup or floating pane placement.
type WindowPosition int

const (
	PosUp     WindowPosition = iota
	PosDown
	PosLeft
	PosRight
	PosCenter
)

// SizeSpec is a size value that may be expressed as a percentage.
type SizeSpec struct {
	Size    float64
	Percent bool
}

func (s SizeSpec) String() string {
	if s.Percent {
		return fmt.Sprintf("%d%%", int(s.Size))
	}
	return fmt.Sprintf("%d", int(s.Size))
}

// TmuxDim converts a SizeSpec to an absolute cell count, clamped between
// min 3 (minimum popup footprint including border) and the window dimension.
func TmuxDim(spec SizeSpec, window int) int {
	dim := int(spec.Size)
	if spec.Percent {
		dim = window * dim / 100
	}
	return max(3, min(dim, window))
}

// PopupArgs builds the args for "tmux display-popup" from position and size
// specs. Returns the full slice suitable for exec.Command("tmux", args...).
// -B is added automatically unless border is true.
func PopupArgs(dir string, pos WindowPosition, width, height SizeSpec, border bool) []string {
	args := []string{"display-popup", "-E", "-d", dir}
	if !border {
		args = append(args, "-B")
	}
	switch pos {
	case PosUp:
		args = append(args, "-xC", "-y0")
	case PosDown:
		args = append(args, "-xC", "-y9999")
	case PosLeft:
		args = append(args, "-x0", "-yC")
	case PosRight:
		args = append(args, "-xR", "-yC")
	case PosCenter:
		args = append(args, "-xC", "-yC")
	}
	args = append(args, "-w"+width.String())
	args = append(args, "-h"+height.String())
	return args
}
