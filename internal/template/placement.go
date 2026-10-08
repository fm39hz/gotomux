package template

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Placement pattern: per-pane slots joined by "," within window, windows by "|".
// Slot: "R" = project root, "C0"/"C1"/... = Children(root)[k], missing -> R at bake.
//
// Learned only from freeze (ObservePlacement). Never user-facing.
// Apply uses BestPlacement when confidence ok; else all-root.

// PatternFromPreset maps each pane cwd to R/Ck against root's children at freeze time.
// Trivial all-R returns "" (nothing to learn).
func PatternFromPreset(p *model.Session) string {
	if p == nil || p.Cwd == "" || len(p.Windows) == 0 {
		return ""
	}
	root := filepath.Clean(p.Cwd)
	children := project.Children(root)
	var wins []string
	allR := true
	for _, w := range p.Windows {
		panes := w.Panes
		if len(panes) == 0 {
			panes = []model.Pane{{}}
		}
		var slots []string
		for _, pn := range panes {
			cwd := pn.Cwd
			if cwd == "" {
				cwd = w.Cwd
			}
			if cwd == "" {
				cwd = root
			}
			s := classify.ScopeForPath(cwd, classify.ProjectContext{Root: root, Children: children})
			if s != classify.ScopeRoot {
				allR = false
			}
			if s == classify.ScopeUnknown {
				slots = append(slots, "?")
			} else {
				slots = append(slots, string(s))
			}
		}
		wins = append(wins, strings.Join(slots, ","))
	}
	if allR {
		return ""
	}
	return strings.Join(wins, "|")
}

// ObservePlacement: best-effort learn non-trivial pattern for shapeID from instance.
func ObservePlacement(st store.Storer, shapeID string, p *model.Session) {
	if st == nil || shapeID == "" || p == nil {
		return
	}
	pat := PatternFromPreset(p)
	if pat == "" {
		return
	}
	if err := st.RecordPlacement(shapeID, pat); err != nil {
		log.Printf("record placement: %v", err)
	}
}

// bakeShape materialises shape -> instance: placement slots + inferred split.
// All inference lives here; Load only runs the resulting preset.
// st/shapeID optional - without them, all panes = root, even split.
func bakeShape(st store.Storer, tmpl *model.Session, name, root, shapeID string) *model.Session {
	if root == "" {
		root, _ = os.Getwd()
	}
	root = filepath.Clean(root)
	p := &model.Session{Name: name, Cwd: root}
	if tmpl == nil || len(tmpl.Windows) == 0 {
		tmpl = builtinDefault()
	}

	var slots [][]string
	if st != nil && shapeID != "" {
		if pat, ok := st.BestPlacement(shapeID); ok {
			slots = parsePattern(pat)
		}
	}
	children := project.Children(root)

	for i, w := range tmpl.Windows {
		n := len(w.Panes)
		if n == 0 {
			n = 1
		}
		var winSlots []string
		if i < len(slots) && len(slots[i]) == n {
			winSlots = slots[i]
		}
		pw := model.Window{
			Idx:    i,
			Name:   w.Name,
			Layout: tmux.InferSplit(w.Layout, n),
			Cwd:    root,
		}
		pw.Panes = make([]model.Pane, n)
		for j := 0; j < n; j++ {
			cwd := root
			if winSlots != nil {
				cwd = resolveSlot(root, children, winSlots[j])
			}
			cmd := ""
			if j < len(w.Panes) {
				cmd = w.Panes[j].Cmd
			}
			pw.Panes[j] = model.Pane{Idx: j, Cwd: cwd, Cmd: cmd}
			if j == 0 {
				pw.Cwd = cwd
			}
		}
		p.Windows = append(p.Windows, pw)
	}
	return p
}

func parsePattern(pat string) [][]string {
	if pat == "" {
		return nil
	}
	wins := strings.Split(pat, "|")
	out := make([][]string, len(wins))
	for i, w := range wins {
		out[i] = strings.Split(w, ",")
	}
	return out
}

func resolveSlot(root string, children []string, slot string) string {
	slot = strings.TrimSpace(slot)
	if slot == "" || classify.ScopeRef(slot) == classify.ScopeRoot || classify.ScopeRef(slot) == classify.ScopeUnknown {
		return root
	}
	k, ok := classify.ScopeRef(slot).ChildIndex()
	if !ok {
		return root
	}
	if k >= len(children) {
		return root
	}
	return children[k]
}
