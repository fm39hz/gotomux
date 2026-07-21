package template

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Fork = one window essence unit.
// Learned silently on freeze/stick. Same pattern → same fork key.
// Readable label for JSON files, hashed key for DB.
// Hit counters in DB only — fork label is human-readable everywhere.

// WindowForkLabel returns a human-readable fork identifier like "2|even-vertical|nvim,sh".
func WindowForkLabel(w store.PresetWindow) string {
	n := len(w.Panes)
	if n == 0 {
		n = 1
	}
	split := tmux.LayoutForShape(w.Layout, n)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d", n))
	b.WriteByte('|')
	b.WriteString(split)
	b.WriteByte('|')
	for j := 0; j < n; j++ {
		if j > 0 {
			b.WriteByte(',')
		}
		if j < len(w.Panes) {
			b.WriteString(tmux.ToolIntent(w.Panes[j].Cmd))
		}
	}
	return b.String()
}

// WindowForkKey returns a stable hash key for DB storage.
func WindowForkKey(w store.PresetWindow) string {
	label := WindowForkLabel(w)
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:8])
}

// WindowForkBody — JSON fragment for DB storage (product, readable, no cwd).
func WindowForkBody(w store.PresetWindow) string {
	n := len(w.Panes)
	if n == 0 {
		n = 1
	}
	type pane struct {
		Cmd string `json:"cmd,omitempty"`
	}
	type win struct {
		Fork  string `json:"fork,omitempty"`
		Split string `json:"split,omitempty"`
		Panes []pane `json:"panes"`
	}
	ww := win{
		Fork:  WindowForkLabel(w),
		Split: tmux.LayoutForShape(w.Layout, n),
	}
	for j := 0; j < n; j++ {
		var c string
		if j < len(w.Panes) {
			c = tmux.ToolIntent(w.Panes[j].Cmd)
		}
		ww.Panes = append(ww.Panes, pane{Cmd: c})
	}
	b, err := json.Marshal(ww)
	if err != nil {
		return ""
	}
	return string(b)
}

// ObserveForks records every window as a fork unit.
func ObserveForks(st *store.Store, p *store.Preset) {
	if st == nil || p == nil {
		return
	}
	sh := ToShape(p, "fork")
	for _, w := range sh.Windows {
		key := WindowForkKey(w)
		if key == "" {
			continue
		}
		_ = st.RecordFork(key, WindowForkBody(w))
	}
}
