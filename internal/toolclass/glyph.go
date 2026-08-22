// Glyph codepoints: the single home for every Nerd Font signature the TUI
// draws. Codepoints are verified against ryanoasis/nerd-fonts generated CSS
// (nf-fa-* Font Awesome 4, nf-dev-* devicons, nf-md-* Material Design).
// Keep every \u/\U escape in this file; other packages reference the symbols.
//
// Outside the BMP, Go escapes require \U plus 8 hex digits (\U000F0403), not
// the 4-digit \u form.
package toolclass

const (
	GlyphSearch = ""
	GlyphCaretRight = ""
	GlyphPin = "󰐃"
	GlyphEditor = ""
	GlyphFolder = ""
	GlyphShell = ""
	GlyphAgent = ""
	GlyphGit = ""
)

// NerdIcon returns a nerd-font glyph for a tool or role token; empty if unknown.
// Callers decide ASCII fallback.
func NerdIcon(tok string) string {
	tok = Base(tok)
	if tok == "" {
		return ""
	}
	// role aliases
	switch tok {
	case "editor":
		return GlyphEditor
	case "files", "file":
		return GlyphFolder
	case "shell", "sh", "term", "terminal":
		return GlyphShell
	case "agent":
		return GlyphAgent
	case "git":
		return GlyphGit
	}
	switch Classify(tok) {
	case KindEditor:
		return GlyphEditor
	case KindFiles:
		return GlyphFolder
	case KindGit:
		return GlyphGit
	case KindAgent:
		return GlyphAgent
	case KindShell:
		return GlyphShell
	default:
		return ""
	}
}
