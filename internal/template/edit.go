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

// JSON edit format (pretty-printed for $EDITOR):
//
//	{
//	  "name": "my-session",
//	  "cwd": "/path",
//	  "windows": [
//	    {
//	      "name": "editor",
//	      "cwd": "/path",
//	      "layout": "even-horizontal",
//	      "panes": [
//	        {"cwd": "/path", "cmd": "nvim"},
//	        {"cwd": "/path"}
//	      ]
//	    }
//	  ]
//	}
//
// layout: named (even-horizontal|…) or tmux window_layout dump from freeze.
// empty/missing → even-horizontal when panes > 1.

type presetJSON struct {
	Name    string       `json:"name"`
	Cwd     string       `json:"cwd,omitempty"`
	Windows []windowJSON `json:"windows"`
}

type windowJSON struct {
	Name   string     `json:"name,omitempty"`
	Cwd    string     `json:"cwd,omitempty"`
	Layout string     `json:"layout,omitempty"`
	Panes  []paneJSON `json:"panes"`
}

type paneJSON struct {
	Cwd string `json:"cwd,omitempty"`
	Cmd string `json:"cmd,omitempty"`
}

// Format writes compact JSON for edit/hand-edit.
// Omits cwd when it equals session root (or empty for pure shapes).
// Pure shapes (no abs root): empty pane cwd omitted; cmds stripped by ToShape before Format.
func Format(p *store.Preset) string {
	root := p.Cwd
	j := presetJSON{Name: p.Name}
	if root != "" {
		j.Cwd = root
	}
	for _, w := range p.Windows {
		wj := windowJSON{
			Name:   w.Name,
			Layout: tmux.LayoutForStore(w.Layout, len(w.Panes)),
		}
		// window cwd only if different from session root
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
			// omit cwd when same as session root or empty (shape relative "")
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
	if j.Name == "" {
		return nil, fmt.Errorf("missing name")
	}
	if !project.ValidSessionName(j.Name) {
		return nil, fmt.Errorf("invalid name %q (no colon/control)", j.Name)
	}
	if len(j.Windows) == 0 {
		return nil, fmt.Errorf("need at least one window")
	}
	p := &store.Preset{Name: j.Name, Cwd: j.Cwd}
	for i, w := range j.Windows {
		if w.Layout != "" && !tmux.IsNamedLayout(w.Layout) && !tmux.IsLayoutDump(w.Layout) {
			return nil, fmt.Errorf("window %d: layout %q (use named layout or tmux window_layout dump)", i, w.Layout)
		}
		pw := store.PresetWindow{
			Idx:    i,
			Name:   w.Name,
			Cwd:    w.Cwd,
			Layout: w.Layout,
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
			return fmt.Errorf("no presets — freeze one first")
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
	// editorCommand already attaches /dev/tty when present (tmux run-shell / popup)
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
	if err := st.Save(np); err != nil {
		return err
	}
	if np.Name != oldName {
		_ = st.Delete(oldName)
	}
	outDir, _ := store.DataDir()
	fmt.Println("saved", np.Name, "→", filepath.Join(outDir, "state.db"))
	return nil
}

func editorCommand(path string) *exec.Cmd {
	var cmd *exec.Cmd
	if srv := os.Getenv("NVIM"); srv != "" {
		cmd = exec.Command("nvim", "--server", srv, "--remote-wait", path)
	} else {
		ed := os.Getenv("EDITOR")
		if ed == "" {
			ed = os.Getenv("VISUAL")
		}
		if ed == "" {
			ed = "nvim"
		}
		if fields := strings.Fields(ed); len(fields) > 1 {
			cmd = exec.Command(fields[0], append(fields[1:], path)...)
		} else {
			cmd = exec.Command(ed, path)
		}
	}
	// tmux run-shell: stdin is not a TTY — attach controlling terminal when possible
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		cmd.Stdin = tty
		cmd.Stdout = tty
		cmd.Stderr = tty
	}
	return cmd
}
