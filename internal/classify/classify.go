// Package classify provides the shared, side-effect-free pane classification
// boundary used by shape learning, placement and reconciliation.
package classify

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/toolclass"
)

type ScopeRef string

const (
	ScopeUnknown ScopeRef = ""
	ScopeRoot    ScopeRef = "R"
)

func ChildScope(index int) ScopeRef {
	if index < 0 {
		return ScopeUnknown
	}
	return ScopeRef("C" + strconv.Itoa(index))
}

func (s ScopeRef) ChildIndex() (int, bool) {
	if len(s) < 2 || s[0] != 'C' {
		return 0, false
	}
	idx, err := strconv.Atoi(string(s[1:]))
	if err != nil || idx < 0 || strconv.Itoa(idx) != string(s[1:]) {
		return 0, false
	}
	return idx, true
}

type ProjectContext struct {
	Root     string
	Children []string
}

type PaneClass struct {
	Tool  string
	Kind  toolclass.Kind
	Scope ScopeRef
}

func ClassifyPane(command, effectiveCwd string, ctx ProjectContext) PaneClass {
	tool := toolclass.Intent(command)
	kind := toolclass.KindUnknown
	if tool != "" {
		kind = toolclass.Classify(tool)
	}
	return PaneClass{Tool: tool, Kind: kind, Scope: ScopeForPath(effectiveCwd, ctx)}
}

func (p PaneClass) PortableClass() string {
	return toolclass.ClassLabel(p.Tool)
}

func ScopeForPath(path string, ctx ProjectContext) ScopeRef {
	if path == "" || ctx.Root == "" {
		return ScopeUnknown
	}
	path, root := filepath.Clean(path), filepath.Clean(ctx.Root)
	if path == root {
		return ScopeRoot
	}
	if !within(root, path) {
		return ScopeUnknown
	}

	best, bestLen := -1, -1
	for i, child := range ctx.Children {
		if child == "" {
			continue
		}
		child = filepath.Clean(child)
		if (path == child || within(child, path)) && len(child) > bestLen {
			best, bestLen = i, len(child)
		}
	}
	if best >= 0 {
		return ChildScope(best)
	}
	return ScopeRoot
}

// StableScopeKey is a matching hint, not a serialized shape identity. It uses
// the path relative to the project root so a reordered Children list cannot
// change identity evidence. Unknown/outside paths stay unknown.
func StableScopeKey(path string, ctx ProjectContext) string {
	scope := ScopeForPath(path, ctx)
	if scope == ScopeUnknown {
		return ""
	}
	if scope == ScopeRoot {
		return "R"
	}
	idx, ok := scope.ChildIndex()
	if !ok || idx >= len(ctx.Children) {
		return ""
	}
	child := filepath.Clean(ctx.Children[idx])
	root := filepath.Clean(ctx.Root)
	rel, err := filepath.Rel(root, child)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return fmt.Sprintf("C:%s", filepath.ToSlash(rel))
}

func within(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
