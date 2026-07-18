package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fm39hz/gotomux/internal/picker"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// version set by dist/PKGBUILD: -ldflags "-X main.version=..."
var version = "dev"


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
			fmt.Printf(`gotomux — session picker (go to mux) (%s)

Usage:
  gotomux              interactive picker
  gotomux -f           freeze session (current if in tmux, else pick) → sqlite
  gotomux -e [name]    edit preset in $EDITOR

Keys (fzf-style combobox — type to filter anytime):
  type          filter
  ctrl-n/p      next/prev (also ↑/↓)
  enter         connect
  ctrl-x        kill active
  ctrl-f        freeze → sqlite
  ctrl-e        edit preset
  ctrl-d        delete preset
  ctrl-t        sticky shape from selection (create/zox use it)
  ctrl-u        clear query
  esc           quit

Store: $XDG_DATA_HOME/gotomux/state.db  (presets, shapes, sticky, usage)
	Optional seed: $XDG_CONFIG_HOME/gotomux/layouts/*.json
Edit format: JSON {name,cwd,windows:[{name,layout,panes:[{cwd,cmd}]}]}
`, version)
			return
		}
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctl, err := tmux.New()
	if err != nil {
		return err
	}
	st, err := store.Open()
	if err != nil {
		return err
	}
	defer st.Close()
	picker.BindStore(st)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := project.FindProjectRoot(cwd)
	name := project.SessionName(root)

	m := picker.NewModel(ctl, st, name, root)
	opts, alt, err := picker.TeaOpts()
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, opts...)
	final, err := p.Run()
	if fm, ok := final.(interface {
		Done() picker.Result
		FrameLines() int
	}); ok {
		if !alt {
			picker.ClearInline(fm.FrameLines())
		}
		if err != nil {
			return err
		}
		res := fm.Done()
		if res.Action != picker.ActionConnect {
			return nil
		}
		return connectItem(ctl, st, res.Item)
	}
	return err
}

func connectItem(ctl *tmux.Ctl, st *store.Store, it picker.Item) error {
	var err error
	switch it.Kind {
	case picker.KindCreate, picker.KindZoxide:
		err = template.ConnectProject(ctl, st, it.Name, it.Path)
	case picker.KindActive:
		err = ctl.Connect(it.Name, "")
	case picker.KindPreset:
		p, errGet := st.Get(it.Name)
		if errGet != nil {
			return errGet
		}
		_ = st.Touch(it.Name)
		err = ctl.ConnectPreset(p)
	default:
		return fmt.Errorf("unknown item kind")
	}
	if err == nil && st != nil {
		_ = st.RecordOpen(it.Name)
		if live, e := ctl.ListLive(); e == nil {
			names := make([]string, 0, len(live))
			for _, s := range live {
				if s.Name != it.Name {
					names = append(names, s.Name)
				}
			}
			st.RecordPairsWithLive(it.Name, names)
		}
	}
	return err
}

func freezeCLI() error {
	ctl, err := tmux.New()
	if err != nil {
		return err
	}
	st, err := store.Open()
	if err != nil {
		return err
	}
	defer st.Close()

	name := ctl.CurrentSession()
	if name == "" {
		live, err := ctl.ListLive()
		if err != nil {
			return err
		}
		if len(live) == 0 {
			return fmt.Errorf("no active sessions")
		}
		items := make([]string, 0, len(live))
		for _, s := range live {
			items = append(items, s.Name)
		}
		name, err = picker.Pick(items)
		if err != nil || name == "" {
			return err
		}
	}
	p, err := ctl.Freeze(name)
	if err != nil {
		return err
	}
	sid, created, err := template.FreezeSave(st, p, false)
	if err != nil {
		return err
	}
	dir, _ := store.DataDir()
	msg := fmt.Sprintf("froze %s → %s", name, filepath.Join(dir, "state.db"))
	if sid != "" {
		if created {
			msg += " · shape " + sid
		} else {
			msg += " · shape " + sid + " (exists)"
		}
	}
	fmt.Println(msg)
	return nil
}

func editCLI(name string) error {
	st, err := store.Open()
	if err != nil {
		return err
	}
	defer st.Close()

	ctl, _ := tmux.New()
	// run-shell has no TTY for fzf pick — default to current tmux session.
	if name == "" && ctl != nil {
		name = ctl.CurrentSession()
	}
	if name == "" {
		// only interactive pick when we can open a TTY
		if _, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err != nil {
			return fmt.Errorf("gotomux -e: pass a session name, or run inside tmux (run-shell has no TTY for picker)")
		}
		return template.Edit(st, "", picker.Pick)
	}

	// no preset yet → freeze current layout into DB first (like ctrl-e on Active)
	if _, err := st.Get(name); err != nil {
		if ctl == nil {
			return fmt.Errorf("preset %q not found and no tmux", name)
		}
		p, err := ctl.Freeze(name)
		if err != nil {
			return err
		}
		if _, _, err := template.FreezeSave(st, p, false); err != nil {
			return err
		}
	}
	return template.Edit(st, name, picker.Pick)
}
