package template

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// JSON format for shapes/presets - human product vocabulary only.
//
// Shape (essence - freeze/sticky mirror under shapes/<id>.json):
//
//	{
//	  "name": "shape-...",
//	  "windows": [
//	    {"name": "editor", "panes": [{"cmd": "nvim"}]},
//	    {"name": "shell", "split": "even-vertical", "panes": [{}, {}]},
//	    {"name": "yazi", "panes": [{"cmd": "yazi"}]}
//	  ]
//	}
//
//	split: even-horizontal | even-vertical | main-* | tiled
//	       omit -> bake infers even-horizontal when panes > 1
//	cmd:   tool intent on that pane (nvim, yazi, opencode, ...); omit = shell
//	never: tmux dump strings, abs paths, % ratios, cwd on pure shapes
//
// Instance presets may add "cwd" for restore-that-session only.
// Legacy key "layout" accepted on parse; dumps classified to split class.

type presetJSON struct {
	// Shape product: id (stable) + label (human). Instance: name = session.
	ID      string       `json:"id,omitempty"`
	Label   string       `json:"label,omitempty"`
	Name    string       `json:"name,omitempty"`
	Cwd     string       `json:"cwd,omitempty"`
	Windows []windowJSON `json:"windows"`
}

type windowJSON struct {
	Fork  string     `json:"fork,omitempty"`
	Name  string     `json:"name,omitempty"`
	Cwd   string     `json:"cwd,omitempty"`
	Split string     `json:"split,omitempty"`
	// legacyLayout accepts old hand-edits / mirrors that still say "layout".
	LegacyLayout string     `json:"layout,omitempty"`
	Panes        []paneJSON `json:"panes"`
}

type paneJSON struct {
	Cwd string `json:"cwd,omitempty"`
	Cmd string `json:"cmd,omitempty"`
}

func (w windowJSON) splitValue() string {
	if w.Split != "" {
		return w.Split
	}
	return w.LegacyLayout
}

// Format writes compact JSON for edit/hand-edit.
// Omits cwd when it equals session root (or empty for pure shapes).
// Window split uses product key "split" (not tmux's "layout").
func Format(p *store.Preset) string {
	root := p.Cwd
	j := presetJSON{}
	if isShapeID(p.Name) || (p.Cwd == "" && looksLikeShapeTree(p)) {
		// pure shape: id inside, label for humans
		j.ID = p.Name
		if j.ID == "" || j.ID == "tmp" {
			j.ID = "shape"
		}
		j.Label = ShapeLabel(p)
		// keep name = label for legacy readers; id is canonical
		j.Name = j.Label
	} else {
		j.Name = p.Name
		if root != "" {
			j.Cwd = root
		}
	}
	for _, w := range p.Windows {
		wj := windowJSON{
			Fork:  WindowForkLabel(w),
			Name:  w.Name,
			Split: tmux.LayoutForShape(w.Layout, len(w.Panes)),
		}
		if w.Cwd != "" && w.Cwd != root {
			wj.Cwd = w.Cwd
		}
		panes := w.Panes
		if len(panes) == 0 {
			panes = []store.PresetPane{{}}
		}
		for _, pn := range panes {
			pj := paneJSON{Cmd: pn.Cmd}
			cwd := pn.Cwd
			if cwd == "" {
				cwd = w.Cwd
			}
			if cwd != "" && cwd != root {
				pj.Cwd = cwd
			}
			wj.Panes = append(wj.Panes, pj)
		}
		j.Windows = append(j.Windows, wj)
	}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"name":%q}`, p.Name)
	}
	return string(b) + "\n"
}

func Parse(text string) (*store.Preset, error) {
	var j presetJSON
	if err := json.Unmarshal([]byte(text), &j); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	name := j.Name
	if j.ID != "" {
		// shape file: runtime identity is id; label is display
		name = j.ID
	}
	if name == "" && j.Label != "" {
		name = j.Label
	}
	if name == "" {
		return nil, fmt.Errorf("missing id/name")
	}
	// shape ids and labels are both safe; session names stricter
	if j.ID == "" && !project.ValidSessionName(name) {
		return nil, fmt.Errorf("invalid name %q (no colon/control)", name)
	}
	if len(j.Windows) == 0 {
		return nil, fmt.Errorf("need at least one window")
	}
	p := &store.Preset{Name: name, Cwd: j.Cwd}
	for i, w := range j.Windows {
		nPanes := len(w.Panes)
		if nPanes == 0 {
			nPanes = 1
		}
		split := w.splitValue()
		if split != "" && !tmux.IsNamedLayout(split) {
			// tmux dump -> portable class; never keep pixel soup in product format
			if tmux.IsLayoutDump(split) {
				split = tmux.LayoutForShape(split, nPanes)
			} else {
				return nil, fmt.Errorf("window %d: split %q (even-horizontal|even-vertical|main-*|tiled or omit)", i, split)
			}
		}
		pw := store.PresetWindow{
			Idx:    i,
			Name:   w.Name,
			Cwd:    w.Cwd,
			Layout: split,
		}
		if len(w.Panes) == 0 {
			cwd := w.Cwd
			if cwd == "" {
				cwd = p.Cwd
			}
			pw.Panes = []store.PresetPane{{Cwd: cwd}}
		} else {
			for k, pn := range w.Panes {
				cwd := pn.Cwd
				if cwd == "" {
					cwd = w.Cwd
				}
				if cwd == "" {
					cwd = p.Cwd
				}
				pw.Panes = append(pw.Panes, store.PresetPane{Idx: k, Cwd: cwd, Cmd: pn.Cmd})
			}
		}
		if pw.Cwd == "" && len(pw.Panes) > 0 {
			pw.Cwd = pw.Panes[0].Cwd
		}
		p.Windows = append(p.Windows, pw)
	}
	return p, nil
}


func isShapeID(s string) bool {
	return s == "default" || strings.HasPrefix(s, "shape-")
}

func looksLikeShapeTree(p *store.Preset) bool {
	if p == nil || p.Cwd != "" {
		return false
	}
	for _, w := range p.Windows {
		if w.Cwd != "" {
			return false
		}
		for _, pn := range w.Panes {
			if pn.Cwd != "" {
				return false
			}
		}
	}
	return len(p.Windows) > 0
}

// CommitEdit saves preset and, on rename, deletes old name + rebinds ranking telemetry.
func CommitEdit(st *store.Store, oldName string, np *store.Preset) error {
	if st == nil || np == nil {
		return fmt.Errorf("commit edit: nil store or preset")
	}
	if err := st.Save(np); err != nil {
		return err
	}
	if oldName != "" && np.Name != oldName {
		_ = st.Delete(oldName)
		_ = st.RebindName(oldName, np.Name)
	}
	return nil
}

// Edit opens preset JSON in $EDITOR (or nvim).
// For tmux binds: use display-popup so the editor has a TTY; -e defaults to current session in main.
func Edit(st *store.Store, name string, pick func([]string) (string, error)) error {
	var p *store.Preset
	var err error
	if name != "" {
		p, err = st.Get(name)
		if err != nil {
			return fmt.Errorf("preset %q: %w", name, err)
		}
	} else {
		names, err := st.ListNames()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("no presets - freeze one first")
		}
		picked, err := pick(names)
		if err != nil || picked == "" {
			return err
		}
		p, err = st.Get(picked)
		if err != nil {
			return err
		}
	}

	oldName := p.Name
	dir, err := store.DataDir()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "edit-*.json")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)

	if _, err := tmp.WriteString(Format(p)); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	cmd := editorCommand(path)
	if cmd.Stdin == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	np, err := Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if err := CommitEdit(st, oldName, np); err != nil {
		return err
	}
	outDir, _ := store.DataDir()
	fmt.Println("saved", np.Name, "->", filepath.Join(outDir, "state.db"))
	return nil
}

func editorCommand(path string) *exec.Cmd {
	ed := os.Getenv("EDITOR")
	if ed == "" {
		ed = os.Getenv("VISUAL")
	}
	if ed == "" {
		ed = "nvim"
	}
	if fields := strings.Fields(ed); len(fields) > 1 {
		return exec.Command(fields[0], append(fields[1:], path)...)
	}
	return exec.Command(ed, path)
}
