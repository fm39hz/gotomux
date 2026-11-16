// Package tmuxlayout parses tmux's layout notation without performing tmux I/O.
package tmuxlayout

import (
	"encoding/json"
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
	if strings.HasPrefix(strings.TrimSpace(layout), "{") {
		return jsonTopology(layout)
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

func jsonTopology(layout string) string {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(layout), &root); err != nil {
		return ""
	}
	rawTree, ok := root["L"]
	if !ok {
		return ""
	}
	var nodeTopology func(json.RawMessage) (string, bool)
	nodeTopology = func(raw json.RawMessage) (string, bool) {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			return "", false
		}
		var nodeType string
		if err := json.Unmarshal(node["t"], &nodeType); err != nil {
			return "", false
		}
		if nodeType == "p" {
			return "_", true
		}
		if nodeType != "h" && nodeType != "v" {
			return "", false
		}
		var children []json.RawMessage
		if err := json.Unmarshal(node["c"], &children); err != nil || len(children) < 2 {
			return "", false
		}
		parts := make([]string, 0, len(children))
		for _, child := range children {
			part, ok := nodeTopology(child)
			if !ok {
				return "", false
			}
			parts = append(parts, part)
		}
		return nodeType + "(" + strings.Join(parts, ",") + ")", true
	}
	topology, ok := nodeTopology(rawTree)
	if !ok {
		return ""
	}
	return topology
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

// PaneBinding identifies the pane identifier and pane index stored on one JSON
// layout leaf. The identifier can be a tmux runtime ID or a durable slot ID.
type PaneBinding struct {
	RuntimeID string
	Index     int
}

// PaneBindings returns pane identifiers and indexes in left-to-right,
// top-to-bottom layout leaf order. Layout formats without embedded IDs return
// a nil slice.
func PaneBindings(layout string) ([]PaneBinding, error) {
	trimmed := strings.TrimSpace(layout)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
		return nil, fmt.Errorf("parse tmux JSON layout: %w", err)
	}
	rawTree, ok := root["L"]
	if !ok {
		return nil, fmt.Errorf("tmux JSON layout has no root tree")
	}
	var bindings []PaneBinding
	var visit func(json.RawMessage) error
	visit = func(raw json.RawMessage) error {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			return fmt.Errorf("parse tmux JSON layout node: %w", err)
		}
		var nodeType string
		if err := json.Unmarshal(node["t"], &nodeType); err != nil {
			return fmt.Errorf("tmux JSON layout node has no type: %w", err)
		}
		if nodeType == "p" {
			var id string
			if err := json.Unmarshal(node["I"], &id); err != nil || id == "" {
				return fmt.Errorf("tmux JSON pane leaf has no runtime ID")
			}
			var index int
			if err := json.Unmarshal(node["i"], &index); err != nil {
				return fmt.Errorf("tmux JSON pane leaf has no pane index: %w", err)
			}
			bindings = append(bindings, PaneBinding{RuntimeID: id, Index: index})
			return nil
		}
		var children []json.RawMessage
		if err := json.Unmarshal(node["c"], &children); err != nil || len(children) == 0 {
			return fmt.Errorf("tmux JSON split node has no children")
		}
		for _, child := range children {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(rawTree); err != nil {
		return nil, err
	}
	return bindings, nil
}

// PaneIDs returns pane identifiers in layout leaf order.
func PaneIDs(layout string) ([]string, error) {
	bindings, err := PaneBindings(layout)
	if err != nil || bindings == nil {
		return nil, err
	}
	ids := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		ids = append(ids, binding.RuntimeID)
	}
	return ids, nil
}

// RemapPaneIDs rewrites pane identifiers embedded in tmux's JSON layout format.
// Legacy checksum layouts and named layouts contain no runtime pane IDs and
// are returned unchanged. A JSON leaf without an explicit replacement is an
// error: applying a partly stale dump can place surviving panes in wrong slots.
func RemapPaneIDs(layout string, paneIDs map[string]string) (string, error) {
	return RemapPaneBindings(layout, paneIDs, nil)
}

// RemapPaneIndexes rewrites the pane-index field on tmux JSON layout leaves.
// tmux uses these indices to place panes when applying a saved layout; pane
// runtime IDs alone do not preserve slot-to-position association.
func RemapPaneIndexes(layout string, paneIndexes map[int]int) (string, error) {
	return RemapPaneBindings(layout, nil, paneIndexes)
}

// RemapPaneBindings rewrites both runtime pane IDs and pane indexes embedded
// in tmux JSON layout leaves. Supplying nil for either map leaves that field
// unchanged. Layout formats without embedded runtime pane IDs are returned
// unchanged.
func RemapPaneBindings(layout string, paneIDs map[string]string, paneIndexes map[int]int) (string, error) {
	trimmed := strings.TrimSpace(layout)
	if !strings.HasPrefix(trimmed, "{") {
		return layout, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
		return "", fmt.Errorf("parse tmux JSON layout: %w", err)
	}
	rawTree, ok := root["L"]
	if !ok {
		return "", fmt.Errorf("tmux JSON layout has no root tree")
	}
	var remapNode func(json.RawMessage) (json.RawMessage, error)
	remapNode = func(raw json.RawMessage) (json.RawMessage, error) {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, fmt.Errorf("parse tmux JSON layout node: %w", err)
		}
		var nodeType string
		if err := json.Unmarshal(node["t"], &nodeType); err != nil {
			return nil, fmt.Errorf("tmux JSON layout node has no type: %w", err)
		}
		if nodeType == "p" {
			if paneIDs != nil {
				var oldID string
				if err := json.Unmarshal(node["I"], &oldID); err != nil || oldID == "" {
					return nil, fmt.Errorf("tmux JSON pane leaf has no runtime ID")
				}
				newID, exists := paneIDs[oldID]
				if !exists || newID == "" {
					return nil, fmt.Errorf("tmux JSON layout pane %s has no live slot binding", oldID)
				}
				encoded, err := json.Marshal(newID)
				if err != nil {
					return nil, err
				}
				node["I"] = encoded
			}
			if paneIndexes != nil {
				var oldIndex int
				if err := json.Unmarshal(node["i"], &oldIndex); err != nil {
					return nil, fmt.Errorf("tmux JSON pane leaf has no pane index: %w", err)
				}
				newIndex, exists := paneIndexes[oldIndex]
				if !exists {
					return nil, fmt.Errorf("tmux JSON layout pane index %d has no live slot binding", oldIndex)
				}
				encoded, err := json.Marshal(newIndex)
				if err != nil {
					return nil, err
				}
				node["i"] = encoded
			}
		} else if rawChildren, exists := node["c"]; exists {
			var children []json.RawMessage
			if err := json.Unmarshal(rawChildren, &children); err != nil {
				return nil, fmt.Errorf("parse tmux JSON layout children: %w", err)
			}
			for i := range children {
				child, err := remapNode(children[i])
				if err != nil {
					return nil, err
				}
				children[i] = child
			}
			encoded, err := json.Marshal(children)
			if err != nil {
				return nil, err
			}
			node["c"] = encoded
		}
		encoded, err := json.Marshal(node)
		if err != nil {
			return nil, err
		}
		return encoded, nil
	}
	tree, err := remapNode(rawTree)
	if err != nil {
		return "", err
	}
	root["L"] = tree
	encoded, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// RemapPaneBindingsByIndex binds JSON layout leaves to a stable model tree by
// each leaf's recorded pane index. This repairs old JSON dumps whose embedded
// runtime IDs belong to a tmux session that has since been recreated.
func RemapPaneBindingsByIndex(layout string, paneIDsByIndex map[int]string, paneIndexes map[int]int) (string, error) {
	bindings, err := PaneBindings(layout)
	if err != nil || bindings == nil {
		return layout, err
	}
	ids := make(map[string]string, len(bindings))
	var indexes map[int]int
	if paneIndexes != nil {
		indexes = make(map[int]int, len(bindings))
	}
	for _, binding := range bindings {
		id, ok := paneIDsByIndex[binding.Index]
		if !ok || id == "" {
			return "", fmt.Errorf("tmux JSON layout pane %s at index %d has no stable slot binding", binding.RuntimeID, binding.Index)
		}
		ids[binding.RuntimeID] = id
		if indexes != nil {
			index, ok := paneIndexes[binding.Index]
			if !ok {
				return "", fmt.Errorf("tmux JSON layout pane index %d has no live index binding", binding.Index)
			}
			indexes[binding.Index] = index
		}
	}
	return RemapPaneBindings(layout, ids, indexes)
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
