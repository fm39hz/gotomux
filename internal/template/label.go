package template

import (
	"fmt"
	"strings"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// ShapeLabel builds a short human slug from shape essence (algorithmic — no ML).
// Examples: "nvim+v2+yazi", "nvim+t4+yazi+opencode", "default"
//
// Rules:
//   - one pane + tool  → tool name (nvim, yazi, opencode, …)
//   - multi pane, no tool → split class short: v2 / h2 / t4 / pN
//   - multi + tools    → tool(s) + count if needed
//   - join windows with "+"
//   - path/session noise already stripped by ToShape roles
func ShapeLabel(p *store.Preset) string {
	if p == nil || len(p.Windows) == 0 {
		return "empty"
	}
	if p.Name == "default" {
		// builtin 2×1 empty → keep stable word
		if len(p.Windows) == 2 && len(p.Windows[0].Panes) <= 1 && len(p.Windows[1].Panes) <= 1 {
			if toolOf(p.Windows[0]) == "" && toolOf(p.Windows[1]) == "" {
				return "default"
			}
		}
	}
	var parts []string
	for _, w := range p.Windows {
		parts = append(parts, windowLabel(w))
	}
	lab := strings.Join(parts, "+")
	if lab == "" {
		return "shape"
	}
	// hard cap for UI chrome
	if len(lab) > 48 {
		lab = lab[:47] + "…"
	}
	return lab
}

func windowLabel(w store.PresetWindow) string {
	n := len(w.Panes)
	if n == 0 {
		n = 1
	}
	tools := paneTools(w)
	split := tmux.LayoutForShape(w.Layout, n)

	switch {
	case len(tools) == 1 && n == 1:
		return tools[0]
	case len(tools) == 1 && n > 1:
		return fmt.Sprintf("%s×%d", tools[0], n)
	case len(tools) > 1:
		return strings.Join(tools, "/")
	case n == 1:
		// empty shell pane — prefer short role if any
		if r := roleSlug(w.Name); r != "" && r != "shell" && !strings.HasPrefix(r, "w") {
			return r
		}
		return "sh"
	default:
		// multi-pane shell farm
		return splitSlug(split, n)
	}
}

func paneTools(w store.PresetWindow) []string {
	var out []string
	seen := map[string]bool{}
	for _, pn := range w.Panes {
		t := tmux.ToolIntent(pn.Cmd)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func toolOf(w store.PresetWindow) string {
	ts := paneTools(w)
	if len(ts) == 1 {
		return ts[0]
	}
	return ""
}

func splitSlug(split string, n int) string {
	switch split {
	case "even-vertical":
		return fmt.Sprintf("v%d", n)
	case "even-horizontal":
		return fmt.Sprintf("h%d", n)
	case "tiled":
		return fmt.Sprintf("t%d", n)
	case "main-vertical", "main-horizontal":
		return fmt.Sprintf("m%d", n)
	default:
		return fmt.Sprintf("p%d", n)
	}
}

func roleSlug(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" || strings.HasPrefix(name, "w") && len(name) <= 3 {
		return ""
	}
	// already sanitized roles from ToShape
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LabelFileSlug: filesystem-safe label (keep + as plus is ok; strip /).
func LabelFileSlug(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "shape"
	}
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '+', r == '×':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '/' || r == ' ':
			b.WriteByte('-')
		case r == '…':
			// skip
		}
	}
	s := b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-+")
	if s == "" {
		return "shape"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
