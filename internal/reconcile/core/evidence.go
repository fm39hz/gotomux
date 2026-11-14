package reconcile

import (
	"path/filepath"
	"strings"

	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
	"github.com/fm39hz/gotomux/internal/toolclass"
)

type scoreVector [6]int64

func (a scoreVector) add(b scoreVector) scoreVector {
	for i := range a {
		a[i] += b[i]
	}
	return a
}

func (a scoreVector) sub(b scoreVector) scoreVector {
	for i := range a {
		a[i] -= b[i]
	}
	return a
}

func (a scoreVector) less(b scoreVector) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func (a scoreVector) equal(b scoreVector) bool { return a == b }

var (
	zeroScore      scoreVector
	infiniteScore  = scoreVector{1 << 50, 1 << 50, 1 << 50, 1 << 50, 1 << 50, 1 << 50}
	forbiddenScore = scoreVector{1 << 40}
)

// maxLexAssignment maximizes a sum of evidence vectors lexicographically.
// Dummy rows and columns represent unmatched baseline/live objects.
func maxLexAssignment(scores [][]scoreVector, allowed [][]bool) ([]int, scoreVector) {
	n, m := len(scores), 0
	for _, row := range scores {
		if len(row) > m {
			m = len(row)
		}
	}
	size := n + m
	if size == 0 {
		return nil, zeroScore
	}
	cost := make([][]scoreVector, size+1)
	for i := range cost {
		cost[i] = make([]scoreVector, size+1)
	}
	for i := 0; i < n; i++ {
		for j := 0; j < m; j++ {
			if i < len(allowed) && j < len(allowed[i]) && allowed[i][j] {
				cost[i+1][j+1] = scoreVector{}.sub(scores[i][j])
			} else {
				cost[i+1][j+1] = forbiddenScore
			}
		}
	}
	u, v := make([]scoreVector, size+1), make([]scoreVector, size+1)
	p, way := make([]int, size+1), make([]int, size+1)
	for i := 1; i <= size; i++ {
		p[0] = i
		j0 := 0
		minv := make([]scoreVector, size+1)
		used := make([]bool, size+1)
		for j := 1; j <= size; j++ {
			minv[j] = infiniteScore
		}
		for {
			used[j0] = true
			i0, delta, j1 := p[j0], infiniteScore, 0
			for j := 1; j <= size; j++ {
				if used[j] {
					continue
				}
				cur := cost[i0][j].sub(u[i0]).sub(v[j])
				if cur.less(minv[j]) {
					minv[j], way[j] = cur, j0
				}
				if minv[j].less(delta) {
					delta, j1 = minv[j], j
				}
			}
			for j := 0; j <= size; j++ {
				if used[j] {
					u[p[j]] = u[p[j]].add(delta)
					v[j] = v[j].sub(delta)
				} else {
					minv[j] = minv[j].sub(delta)
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
			if j0 == 0 {
				break
			}
		}
	}
	assignment := make([]int, n)
	for i := range assignment {
		assignment[i] = -1
	}
	total := zeroScore
	for j := 1; j <= size; j++ {
		i, col := p[j]-1, j-1
		if i >= 0 && i < n && col < m && col < len(allowed[i]) && allowed[i][col] {
			assignment[i] = col
			total = total.add(scores[i][col])
		}
	}
	return assignment, total
}

// equivalentSemanticGroup identifies a complete bipartite set whose every
// edge has the same multi-signal evidence vector. These untagged live objects
// are interchangeable under the frozen semantic contract. A deterministic
// pairing preserves every process and restores the baseline slot tags.
func equivalentSemanticGroup(scores [][]scoreVector, allowed [][]bool) bool {
	n, m := len(scores), 0
	for _, row := range scores {
		if len(row) > m {
			m = len(row)
		}
	}
	if n == 0 || m == 0 || n*m <= 1 || len(allowed) != n {
		return false
	}
	first := scores[0][0]
	for i := 0; i < n; i++ {
		if len(scores[i]) != m || len(allowed[i]) != m {
			return false
		}
		for j := 0; j < m; j++ {
			if !allowed[i][j] || !scores[i][j].equal(first) {
				return false
			}
		}
	}
	return true
}

type semanticEvidence struct {
	score     scoreVector
	items     []Evidence
	strong    int
	allowed   bool
	plausible bool
}

func semanticWindowEvidence(base *model.Session, bw model.Window, live *model.Session, lw model.Window) semanticEvidence {
	e := semanticEvidence{}
	baseCmds, liveCmds := make(map[string]bool), make(map[string]bool)
	baseTools, liveTools := make(map[string]bool), make(map[string]bool)
	for _, pane := range bw.Panes {
		if key := executableKey(pane); key != "" {
			baseCmds[key] = true
		}
		if tool := paneIntent(pane); tool != "" {
			baseTools[toolclass.ClassLabel(tool)] = true
		}
	}
	for _, pane := range lw.Panes {
		if key := executableKey(pane); key != "" {
			liveCmds[key] = true
		}
		if tool := paneIntent(pane); tool != "" {
			liveTools[toolclass.ClassLabel(tool)] = true
		}
	}
	if incompatibleToolSets(baseTools, liveTools) || (len(baseTools) == 0 && len(liveTools) > 0 && onlyShellish(bw)) || (len(liveTools) == 0 && len(baseTools) > 0 && onlyShellish(lw)) {
		return e
	}
	if setIntersects(baseCmds, liveCmds) {
		e.score[0] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "executable", Value: "same executable"})
		e.strong++
	}
	if setIntersects(baseTools, liveTools) {
		e.score[1] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "tool", Value: "same tool intent"})
		if e.score[0] == 0 {
			e.strong++
		}
	}
	if samePathScope(bw.Cwd, lw.Cwd, base.Cwd) {
		e.score[2] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "scope", Value: "same project scope"})
		e.strong++
	}
	if len(bw.Panes) == len(lw.Panes) {
		e.score[4] = 1
		layoutEqual, layoutKnown := tmuxlayout.Compare(bw.Layout, len(bw.Panes), lw.Layout, len(lw.Panes))
		if layoutKnown && layoutEqual {
			e.score[3] = 1
			e.strong++
			e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "topology", Value: "same pane topology"})
		} else if len(bw.Panes) <= 1 {
			e.score[3] = 1
			e.strong++
			e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "topology", Value: "same single-pane structure"})
		}
	}
	baseRole, liveRole := semanticWindowRole(*base, bw), semanticWindowRole(*live, lw)
	if baseRole != "" && baseRole == liveRole {
		e.score[5] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "role", Value: baseRole})
	} else if bw.Name != "" && bw.Name == lw.Name {
		e.score[5] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "name", Value: "same display name"})
	}
	e.plausible = e.score != zeroScore
	// A command/tool signal is one group; it cannot count twice as independent
	// evidence. A semantic edge needs a second group such as scope or topology.
	commandGroup := e.score[0] == 1 || e.score[1] == 1
	otherGroups := e.score[2] + e.score[3]
	e.allowed = commandGroup && otherGroups > 0 || e.score[2] == 1 && e.score[3] == 1
	return e
}

func semanticWindowRole(session model.Session, window model.Window) string {
	if window.Role != "" && session.ClassifierVersion == classify.WindowRoleVersion {
		return window.Role
	}
	return classify.WindowRole(window)
}

func semanticPaneEvidence(base *model.Session, bp model.Pane, live *model.Session, lp model.Pane) semanticEvidence {
	e := semanticEvidence{}
	if a, b := executableKey(bp), executableKey(lp); a != "" && a == b {
		e.score[0] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "executable", Value: "same executable"})
	}
	bt, lt := paneIntent(bp), paneIntent(lp)
	bClass, lClass := "", ""
	if bt != "" {
		bClass = toolclass.ClassLabel(bt)
	}
	if lt != "" {
		lClass = toolclass.ClassLabel(lt)
	}
	if (bClass != "" && lClass != "" && bClass != lClass) || (bClass == "" && lClass != "" && onlyShellishPane(bp)) || (lClass == "" && bClass != "" && onlyShellishPane(lp)) {
		return e
	}
	if bClass != "" && bClass == lClass {
		e.score[1] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "tool", Value: "same tool intent"})
	}
	if samePathScope(bp.Cwd, lp.Cwd, base.Cwd) {
		e.score[2] = 1
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "scope", Value: "same project scope"})
	}
	e.plausible = e.score != zeroScore
	commandGroup := e.score[0] == 1 || e.score[1] == 1
	shellPair := bt == "" && lt == "" && onlyShellishPane(bp) && onlyShellishPane(lp)
	if shellPair {
		e.score[3] = 1 // Parent window identity is already resolved by this call site.
		e.items = append(e.items, Evidence{Class: EvidenceSemantic, Signal: "parent-shell-pane", Value: "shell pane within matched window"})
	}
	e.plausible = e.plausible || shellPair
	e.allowed = commandGroup && e.score[2] == 1 || shellPair
	return e
}

func paneIntent(pane model.Pane) string {
	command := pane.Cmd
	if command == "" {
		command = pane.CmdPath
	}
	if command == "" {
		command = pane.StartCmd
	}
	return classify.ClassifyPane(command, pane.Cwd, classify.ProjectContext{}).Tool
}

func incompatibleToolSets(a, b map[string]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return !setIntersects(a, b)
}

func onlyShellish(window model.Window) bool {
	if len(window.Panes) == 0 {
		return true
	}
	for _, pane := range window.Panes {
		command := pane.Cmd
		if command == "" {
			command = pane.CmdPath
		}
		if command == "" {
			command = pane.StartCmd
		}
		if command != "" && !toolclass.IsShell(command) {
			return false
		}
	}
	return true
}

func onlyShellishPane(pane model.Pane) bool {
	command := pane.Cmd
	if command == "" {
		command = pane.CmdPath
	}
	if command == "" {
		command = pane.StartCmd
	}
	return command == "" || toolclass.IsShell(command)
}

func executableKey(p model.Pane) string {
	if p.CmdPath != "" {
		return filepath.Clean(p.CmdPath)
	}
	start := strings.TrimSpace(p.StartCmd)
	if start != "" {
		fields := strings.Fields(start)
		if len(fields) > 0 {
			return filepath.Base(fields[0])
		}
	}
	if p.Cmd != "" {
		return filepath.Base(p.Cmd)
	}
	return ""
}

func samePathScope(a, b, root string) bool {
	if a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ka, kb := pathScopeKey(a, root), pathScopeKey(b, root)
	return ka != "" && ka == kb
}

func pathScopeKey(path, root string) string {
	if path == "" || root == "" {
		return ""
	}
	path, root = filepath.Clean(path), filepath.Clean(root)
	if path == root {
		return "R"
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	first := strings.Split(filepath.ToSlash(rel), "/")[0]
	if first == "" || first == "." || first == ".." {
		return ""
	}
	return "C:" + first
}

func setIntersects(a, b map[string]bool) bool {
	for value := range a {
		if b[value] {
			return true
		}
	}
	return false
}
