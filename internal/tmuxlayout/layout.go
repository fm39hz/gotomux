// Package tmuxlayout parses tmux's layout notation without performing tmux I/O.
package tmuxlayout

import (
	"fmt"
	"strconv"
	"strings"
)

var namedLayouts = map[string]bool{
	"even-horizontal": true,
	"even-vertical":   true,
	"main-horizontal": true,
	"main-vertical":   true,
	"tiled":           true,
}

func IsNamed(s string) bool { return namedLayouts[s] }

func IsDump(s string) bool {
	return strings.Contains(s, ",") && (strings.Contains(s, "{") || strings.Contains(s, "[") || strings.Contains(s, "x"))
}

// Topology removes dimensions, coordinates, checksums and pane IDs from a
// tmux layout dump while retaining split orientation and nesting.
func Topology(layout string) string {
	if !IsDump(layout) {
		return ""
	}
	checksumEnd := strings.IndexByte(layout, ',')
	body := layout[checksumEnd+1:]
	p := layoutTreeParser{src: body}
	tree, err := p.node()
	if err == nil && p.pos == len(p.src) {
		return tree
	}
	var fallback strings.Builder
	for _, r := range body {
		switch r {
		case '[', ']', '{', '}':
			fallback.WriteRune(r)
		}
	}
	return fallback.String()
}

func ForStore(layout string, nPanes int) string {
	if nPanes <= 1 || layout == "" {
		return ""
	}
	if IsNamed(layout) || IsDump(layout) {
		return layout
	}
	return ""
}

func ForShape(layout string, nPanes int) string {
	if nPanes <= 1 {
		return ""
	}
	if IsNamed(layout) {
		return layout
	}
	if IsDump(layout) {
		return classifyDump(layout)
	}
	return ""
}

func InferSplit(layout string, nPanes int) string {
	if nPanes <= 1 {
		return ""
	}
	if IsNamed(layout) {
		return layout
	}
	if IsDump(layout) {
		return classifyDump(layout)
	}
	return "even-horizontal"
}

// Compare reports whether two stored layouts are equivalent and whether the
// representation contains enough information to decide. Unknown multi-pane
// layouts are never reported as equal merely because both are empty.
func Compare(a string, aPanes int, b string, bPanes int) (equal, known bool) {
	if aPanes <= 1 && bPanes <= 1 {
		return aPanes == bPanes, true
	}
	if aPanes != bPanes {
		return false, true
	}
	if IsDump(a) && IsDump(b) {
		return Topology(a) == Topology(b), Topology(a) != ""
	}
	aa, bb := ForShape(a, aPanes), ForShape(b, bPanes)
	if aa == "" || bb == "" {
		return false, false
	}
	return aa == bb, true
}

func Equivalent(a string, aPanes int, b string, bPanes int) bool {
	equal, known := Compare(a, aPanes, b, bPanes)
	return equal && known
}

func classifyDump(dump string) string {
	h := strings.Contains(dump, "{")
	v := strings.Contains(dump, "[")
	switch {
	case h && v:
		return "tiled"
	case v:
		return "even-vertical"
	case h:
		return "even-horizontal"
	default:
		return "even-horizontal"
	}
}

type layoutTreeParser struct {
	src string
	pos int
}

func (p *layoutTreeParser) node() (string, error) {
	for field := 0; field < 3; field++ {
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] != ',' && p.src[p.pos] != '[' && p.src[p.pos] != ']' && p.src[p.pos] != '{' && p.src[p.pos] != '}' {
			p.pos++
		}
		if p.pos == start {
			return "", fmt.Errorf("empty layout node field")
		}
		if field < 2 {
			if p.pos >= len(p.src) || p.src[p.pos] != ',' {
				return "", fmt.Errorf("incomplete layout node header")
			}
			p.pos++
		}
	}
	if p.pos >= len(p.src) {
		return "_", nil
	}
	open := p.src[p.pos]
	if open != '[' && open != '{' {
		if p.src[p.pos] == ',' && p.consumePaneID() {
			return "_", nil
		}
		return "_", nil
	}
	p.pos++
	close := byte(']')
	if open == '{' {
		close = '}'
	}
	children := make([]string, 0, 2)
	for {
		child, err := p.node()
		if err != nil {
			return "", err
		}
		children = append(children, child)
		if p.pos >= len(p.src) {
			return "", fmt.Errorf("unterminated layout node")
		}
		if p.src[p.pos] == close {
			p.pos++
			break
		}
		if p.src[p.pos] != ',' {
			return "", fmt.Errorf("invalid layout child separator")
		}
		p.pos++
	}
	return string(open) + strings.Join(children, ",") + string(close), nil
}

func (p *layoutTreeParser) consumePaneID() bool {
	start := p.pos + 1
	end := start
	for end < len(p.src) && p.src[end] != ',' && p.src[end] != '[' && p.src[end] != ']' && p.src[end] != '{' && p.src[end] != '}' {
		end++
	}
	field := p.src[start:end]
	if field == "" || strings.Contains(field, "x") {
		return false
	}
	if _, err := strconv.Atoi(field); err != nil {
		return false
	}
	p.pos = end
	return true
}
