package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Default template: used when Create/Zoxide starts a session that has no
// live session and no saved preset. Zero extra prompts — enter just works.
//
// Override: $XDG_DATA_HOME/tmux_project/templates/default.json
// (created on first use; same JSON shape as presets, cwd fields relative to project root).

func builtinDefaultTemplate() *Preset {
	return &Preset{
		Name: "default",
		Windows: []PresetWindow{
			{Name: "editor", Panes: []PresetPane{{Cmd: "nvim"}}},
			{Name: "shell", Panes: []PresetPane{{}}},
		},
	}
}

func templatePath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "templates", "default.json"), nil
}

// loadDefaultTemplate reads templates/default.json, or writes the builtin and returns it.
func loadDefaultTemplate() (*Preset, error) {
	path, err := templatePath()
	if err != nil {
		return builtinDefaultTemplate(), nil
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		p, err := parsePreset(string(raw))
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", path, err)
		}
		return p, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	p := builtinDefaultTemplate()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return p, nil // still usable in-memory
	}
	_ = os.WriteFile(path, []byte(formatPreset(p)), 0o644)
	return p, nil
}

// applyTemplate stamps a template onto a project root.
// Empty/relative pane·window cwd → under root; absolute cwd kept.
func applyTemplate(tmpl *Preset, name, root string) *Preset {
	if root == "" {
		root, _ = os.Getwd()
	}
	p := &Preset{Name: name, Cwd: root}
	if tmpl == nil || len(tmpl.Windows) == 0 {
		tmpl = builtinDefaultTemplate()
	}
	for i, w := range tmpl.Windows {
		wcwd := resolveCwd(root, w.Cwd)
		pw := PresetWindow{
			Idx:    i,
			Name:   w.Name,
			Cwd:    wcwd,
			Layout: w.Layout,
		}
		if len(w.Panes) == 0 {
			pw.Panes = []PresetPane{{Cwd: wcwd}}
		} else {
			for j, pn := range w.Panes {
				cwd := pn.Cwd
				if cwd == "" {
					cwd = w.Cwd
				}
				pw.Panes = append(pw.Panes, PresetPane{
					Idx: j,
					Cwd: resolveCwd(root, cwd),
					Cmd: pn.Cmd,
				})
			}
		}
		p.Windows = append(p.Windows, pw)
	}
	return p
}

func resolveCwd(root, cwd string) string {
	if cwd == "" {
		return root
	}
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Join(root, cwd)
}

// connectProject: lowest-friction path for Create / Zoxide.
//
//	session live?  → attach/switch
//	preset saved?  → bake that layout (specialized)
//	else           → default template at cwd
func connectProject(ctl *TmuxCtl, store *Store, name, cwd string) error {
	if ctl.Has(name) {
		return ctl.Connect(name, "")
	}
	if p, err := store.Get(name); err == nil {
		_ = store.Touch(name)
		return ctl.ConnectPreset(p)
	}
	tmpl, err := loadDefaultTemplate()
	if err != nil {
		return err
	}
	return ctl.ConnectPreset(applyTemplate(tmpl, name, cwd))
}
