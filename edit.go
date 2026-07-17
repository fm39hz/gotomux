package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Text format (human):
//
//	# comment
//	name: my-session
//	cwd: /path/to/root
//
//	[window: editor]
//	cwd: /path/to/root
//	layout: even-horizontal   # optional
//	pane: /path/to/root | nvim
//	pane: /path/to/root
//
//	[window: test]
//	pane: /path |
//	pane: /path/test |
//
// pane line:  pane: <cwd> | <cmd>
// cmd empty → default shell

func formatPreset(p *Preset) string {
	var b strings.Builder
	b.WriteString("# tmux_project preset — edit & save, then quit editor\n")
	b.WriteString("# pane: <cwd> | <cmd>   (cmd empty = shell)\n")
	b.WriteString("# [window: name] then pane lines; window cwd optional\n\n")
	b.WriteString("name: " + p.Name + "\n")
	b.WriteString("cwd: " + p.Cwd + "\n")
	for _, w := range p.Windows {
		b.WriteString("\n[window: " + w.Name + "]\n")
		if w.Cwd != "" {
			b.WriteString("cwd: " + w.Cwd + "\n")
		}
		if w.Layout != "" {
			b.WriteString("layout: " + w.Layout + "\n")
		}
		if len(w.Panes) == 0 {
			cwd := w.Cwd
			if cwd == "" {
				cwd = p.Cwd
			}
			b.WriteString("pane: " + cwd + " |\n")
			continue
		}
		for _, pn := range w.Panes {
			cwd := pn.Cwd
			if cwd == "" {
				cwd = w.Cwd
			}
			b.WriteString(fmt.Sprintf("pane: %s | %s\n", cwd, pn.Cmd))
		}
	}
	return b.String()
}

func parsePreset(text string) (*Preset, error) {
	p := &Preset{}
	var cur *PresetWindow
	sc := bufio.NewScanner(strings.NewReader(text))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[window:") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[len("[window:") : len(line)-1])
			p.Windows = append(p.Windows, PresetWindow{Name: name, Idx: len(p.Windows)})
			cur = &p.Windows[len(p.Windows)-1]
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: bad syntax %q", lineNo, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "name":
			if cur != nil {
				return nil, fmt.Errorf("line %d: name only at top level", lineNo)
			}
			p.Name = val
		case "cwd":
			if cur != nil {
				cur.Cwd = val
			} else {
				p.Cwd = val
			}
		case "layout":
			if cur == nil {
				return nil, fmt.Errorf("line %d: layout needs [window:]", lineNo)
			}
			cur.Layout = val
		case "pane":
			if cur == nil {
				return nil, fmt.Errorf("line %d: pane needs [window:]", lineNo)
			}
			cwd, cmd, _ := strings.Cut(val, "|")
			cwd = strings.TrimSpace(cwd)
			cmd = strings.TrimSpace(cmd)
			cur.Panes = append(cur.Panes, PresetPane{
				Idx: len(cur.Panes),
				Cwd: cwd,
				Cmd: cmd,
			})
			if cur.Cwd == "" && cwd != "" {
				cur.Cwd = cwd
			}
		default:
			return nil, fmt.Errorf("line %d: unknown key %q", lineNo, key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, fmt.Errorf("missing name:")
	}
	if len(p.Windows) == 0 {
		return nil, fmt.Errorf("need at least one [window:]")
	}
	for i := range p.Windows {
		if len(p.Windows[i].Panes) == 0 {
			cwd := p.Windows[i].Cwd
			if cwd == "" {
				cwd = p.Cwd
			}
			p.Windows[i].Panes = []PresetPane{{Cwd: cwd}}
		}
	}
	return p, nil
}

func editPreset(store *Store, name string) error {
	var p *Preset
	var err error
	if name != "" {
		p, err = store.Get(name)
		if err != nil {
			return fmt.Errorf("preset %q: %w", name, err)
		}
	} else {
		names, err := store.ListNames()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("no presets — freeze one first")
		}
		items := make([]listName, len(names))
		for i, n := range names {
			items[i] = listName(n)
		}
		picked, err := runPick(items)
		if err != nil || picked == "" {
			return err
		}
		p, err = store.Get(picked)
		if err != nil {
			return err
		}
	}

	oldName := p.Name
	dir, err := dataDir()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "edit-*.tp")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)

	if _, err := tmp.WriteString(formatPreset(p)); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "nvim"
	}
	// EDITOR may be "nvim" or "code -w" — use shell only if spaces; prefer simple
	var cmd *exec.Cmd
	if fields := strings.Fields(editor); len(fields) > 1 {
		cmd = exec.Command(fields[0], append(fields[1:], path)...)
	} else {
		cmd = exec.Command(editor, path)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	np, err := parsePreset(string(raw))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if err := store.Save(np); err != nil {
		return err
	}
	// renamed → drop old row
	if np.Name != oldName {
		_ = store.Delete(oldName)
	}
	fmt.Println("saved", np.Name, "→", filepath.Join(mustDataDir(), "state.db"))
	return nil
}
