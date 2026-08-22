package picker

import (
	"strings"

	"github.com/epilande/go-devicons"
	"github.com/fm39hz/gotomux/internal/toolclass"
)

// TUI chrome may use Nerd Font glyphs when useful.
// Filenames / shape labels on disk stay ASCII (template.ShapeLabel).
//
// icons = "ascii" in config.toml forces plain ASCII.

func (m model) nerdIcons() bool {
	if m.cfg == nil {
		return true // historical default for bare models in tests
	}
	return m.cfg.NerdIcons()
}

func (m model) iconCursor() string {
	if m.nerdIcons() {
		return toolclass.GlyphCaretRight + " "
	}
	return "> "
}

func (m model) iconSticky() string {
	if m.nerdIcons() {
		return toolclass.GlyphPin + " "
	}
	return "sticky:"
}

func iconPrompt() string {
	return ": " // simple consistent prefix
}

// iconForTool maps sticky label tokens to nerd icons.
// Uses toolclass first; files/folder tokens via go-devicons (nvim-web-devicons maps).
func (m model) iconForTool(tok string) string {
	if !m.nerdIcons() {
		return tok
	}
	// split/count tokens stay ascii (v2, t4, h2, pN)
	if len(tok) > 0 && tok[0] >= '0' && tok[0] <= '9' {
		return tok
	}
	if len(tok) >= 2 && (tok[0] == 'v' || tok[0] == 'h' || tok[0] == 't' || tok[0] == 'm' || tok[0] == 'p') {
		allDigit := true
		for _, r := range tok[1:] {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return tok
		}
	}
	if ic := toolclass.NerdIcon(tok); ic != "" {
		return ic
	}
	// go-devicons: treat token as filename (nvim -> nvim, go.mod style)
	style := devicons.IconForPath(tok)
	if style.Icon != "" && style.Icon != "?" {
		return style.Icon
	}
	// directory-ish
	style = devicons.IconForPath(tok + "/")
	if style.Icon != "" && style.Icon != "?" {
		return style.Icon
	}
	return tok
}

// formatStickyMeta: " sticky:nvim+v2+yazi" or pin + nerd icons.
func (m model) formatStickyMeta(label string) string {
	if label == "" || label == "default" {
		return ""
	}
	if !m.nerdIcons() {
		return "  sticky:" + label
	}
	parts := strings.Split(label, "+")
	for i, p := range parts {
		parts[i] = m.iconForTool(p)
	}
	return "  " + m.iconSticky() + strings.Join(parts, " +")
}
