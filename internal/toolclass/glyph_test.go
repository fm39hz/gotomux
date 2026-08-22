package toolclass

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGlyphsLiveOnlyHere pins the single-home rule: glyph.go is the only file
// allowed to carry a Nerd Font codepoint — raw Private-Use rune or \u/\U
// escape. A stray glyph elsewhere is exactly how the caret marker silently
// degraded to a space during a refactor (stripped PUA bytes).
func TestGlyphsLiveOnlyHere(t *testing.T) {
	esc := regexp.MustCompile(`[\\][uU][0-9A-Fa-f]{4,}`)
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || filepath.Base(path) == "glyph.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, r := range string(b) {
			if r >= 0xE000 && r <= 0xF8FF || r >= 0xF0000 && r <= 0xFFFFD {
				t.Errorf("%s holds raw PUA rune U+%04X; move it to glyph.go", path, r)
			}
		}
		if esc.Match(b) {
			t.Errorf("%s holds a \\u/\\U escape; move it to glyph.go", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
