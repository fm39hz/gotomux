package template

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/toolclass"
)

// Fork = multi-window tool-group essence.
// A fork groups several windows into a reusable pattern.
// Same class pattern across windows → same fork key.

// PaneClass returns the fork class name for a pane command.
func PaneClass(cmd string) string {
	base := toolclass.Base(cmd)
	if base == "" {
		return "shell"
	}
	return toolclass.ClassLabel(base)
}

// paneClassSlice returns ordered class names for a window's panes.
func paneClassSlice(w model.Window) []string {
	n := len(w.Panes)
	if n == 0 {
		return []string{"shell"}
	}
	classes := make([]string, n)
	for i := range w.Panes {
		classes[i] = PaneClass(w.Panes[i].Cmd)
	}
	return classes
}

// WindowForkKey returns the class key for ONE window — a building block.
// Examples: "editor", "editor,shell", "shell,shell".
func WindowForkKey(w model.Window) string {
	classes := paneClassSlice(w)
	return strings.Join(classes, ",")
}

// ShapeForkKey returns the multi-window fork key for a whole shape.
// Hashes the ordered window class keys so same class profile = same fork.
func ShapeForkKey(p *model.Session) string {
	sh := ToShape(p, "fork")
	var keys []string
	for _, w := range sh.Windows {
		keys = append(keys, WindowForkKey(w))
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "|")))
	return hex.EncodeToString(sum[:8])
}

// ShapeForkBody returns JSON for the shape-level fork: all windows + example tools.
func ShapeForkBody(p *model.Session) string {
	sh := ToShape(p, "fork")
	type pane struct {
		Tool string `json:"tool,omitempty"`
	}
	type win struct {
		Fork  string `json:"fork"`
		Split string `json:"split,omitempty"`
		Panes []pane `json:"panes"`
	}
	var wins []win
	for _, w := range sh.Windows {
		n := len(w.Panes)
		if n == 0 {
			n = 1
		}
		wj := win{
			Fork:  WindowForkKey(w),
			Split: tmux.LayoutForShape(w.Layout, n),
		}
		for j := 0; j < n; j++ {
			var cmd string
			if j < len(w.Panes) {
				cmd = tmux.ToolIntent(w.Panes[j].Cmd)
			}
			wj.Panes = append(wj.Panes, pane{Tool: cmd})
		}
		wins = append(wins, wj)
	}
	out := struct {
		Key     string `json:"key"`
		NWins   int    `json:"nWindows"`
		Windows []win  `json:"windows"`
	}{
		Key:     ShapeForkKey(p),
		NWins:   len(wins),
		Windows: wins,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// ObserveForks records the whole shape as one multi-window fork unit.
func ObserveForks(st store.Storer, p *model.Session) {
	if st == nil || p == nil {
		return
	}
	key := ShapeForkKey(p)
	if key == "" {
		return
	}
	if err := st.RecordFork(key, ShapeForkBody(p)); err != nil {
		log.Printf("record fork: %v", err)
	}
}

// ForkClassKeyString returns "editor,shell" format from a pane command slice.
func ForkClassKeyString(cmds []string) string {
	classes := make([]string, len(cmds))
	for i, c := range cmds {
		if c == "" {
			classes[i] = "shell"
		} else {
			classes[i] = toolclass.ClassLabel(c)
		}
	}
	return strings.Join(classes, ",")
}
