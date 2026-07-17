package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-f", "--freeze":
			if err := freezeCLI(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "-e", "--edit":
			name := ""
			if len(os.Args) > 2 {
				name = os.Args[2]
			}
			if err := editCLI(name); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "-h", "--help":
			fmt.Println(`tmux_project — session picker

Usage:
  tmux_project              interactive picker
  tmux_project -f           freeze active session → sqlite
  tmux_project -e [name]    edit preset in $EDITOR

Keys (fzf-style combobox — type to filter anytime):
  type          filter
  ctrl-n/p      next/prev (also ↑/↓)
  enter         connect
  ctrl-x        kill active
  ctrl-f        freeze → sqlite
  ctrl-e        edit preset
  ctrl-d        delete preset
  ctrl-u        clear query
  esc           quit

Store: $XDG_DATA_HOME/tmux_project/state.db
Edit format: name/cwd + [window: x] + "pane: <cwd> | <cmd>"`)
			return
		}
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctl, err := newTmuxCtl()
	if err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := findProjectRoot(cwd)
	name := sessionName(root)

	m := newModel(ctl, store, name, root)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	res := final.(model).done
	if res.action != actionConnect {
		return nil
	}
	return connectItem(ctl, store, res.item)
}

func connectItem(ctl *TmuxCtl, store *Store, it item) error {
	switch it.kind {
	case kindCreate:
		return ctl.Connect(it.name, it.path)
	case kindActive:
		return ctl.Connect(it.name, "")
	case kindPreset:
		p, err := store.Get(it.name)
		if err != nil {
			return err
		}
		_ = store.Touch(it.name)
		return ctl.ConnectPreset(p)
	case kindZoxide:
		return ctl.Connect(it.name, it.path)
	default:
		return fmt.Errorf("unknown item kind")
	}
}

func freezeCLI() error {
	ctl, err := newTmuxCtl()
	if err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	live, err := ctl.ListLive()
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return fmt.Errorf("no active sessions")
	}

	items := make([]listName, 0, len(live))
	for _, s := range live {
		items = append(items, listName(s.Name))
	}
	name, err := runPick(items)
	if err != nil || name == "" {
		return err
	}
	p, err := ctl.Freeze(name)
	if err != nil {
		return err
	}
	if err := store.Save(p); err != nil {
		return err
	}
	fmt.Println("froze", name, "→", filepath.Join(mustDataDir(), "state.db"))
	return nil
}

func editCLI(name string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	return editPreset(store, name)
}

func mustDataDir() string {
	d, err := dataDir()
	if err != nil {
		return "~/.local/share/tmux_project"
	}
	return d
}
