package main

import (
	"fmt"
	"os"
	"os/signal"
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
	// Ignore SIGINT (Ctrl+C): Bubble Tea handles ctrl+c as a key; a second
	// SIGINT from spam must not kill the process mid-redraw (nu reports SIGINT).
	signal.Ignore(os.Interrupt)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-f", "--freeze":
			fname := ""
			if len(os.Args) > 2 {
				fname = os.Args[2]
			}
			if err := freezeCLI(fname); err != nil {
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
  gotomux -f [name]    freeze session (arg, else current, else pick) → sqlite
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
	if ctl == nil {
		return fmt.Errorf("connect: nil tmux")
	}
	var err error
	switch it.Kind {
	case picker.KindCreate, picker.KindZoxide:
		err = template.ConnectProject(ctl, st, it.Name, it.Path)
	case picker.KindActive:
		err = ctl.Connect(it.Name, "")
	case picker.KindPreset:
		if st == nil {
			return fmt.Errorf("connect preset: nil store")
		}
		p, errGet := st.Get(it.Name)
		if errGet != nil {
			return fmt.Errorf("preset %q: %w", it.Name, errGet)
		}
		_ = st.Touch(it.Name) // best-effort ranking signal
		err = ctl.ConnectPreset(p)
	default:
		return fmt.Errorf("unknown item kind %v", it.Kind)
	}
	if err != nil {
		return err
	}
	// ranking telemetry — never fail the connect
	if st != nil {
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
	return nil
}

func freezeCLI(name string) error {
	ctl, err := tmux.New()
	if err != nil {
		return fmt.Errorf("tmux: %w", err)
	}
	st, err := store.Open()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	if name == "" {
		name = ctl.CurrentSession()
	}
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
		return fmt.Errorf("freeze %q: %w", name, err)
	}
	sid, created, err := template.FreezeSave(st, p, false)
	if err != nil {
		return fmt.Errorf("save freeze %q: %w", name, err)
	}
	dir, err := store.DataDir()
	if err != nil {
		dir = "(state.db)"
	}
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
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctl, ctlErr := tmux.New()
	// Prefer explicit name; else current tmux session (run-shell has no TTY for pick).
	if name == "" && ctlErr == nil && ctl != nil {
		name = ctl.CurrentSession()
	}
	if name == "" {
		if _, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err != nil {
			return fmt.Errorf("edit: need session name or interactive TTY (use display-popup for binds)")
		}
		if err := template.Edit(st, "", picker.Pick); err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		return nil
	}

	// no preset yet → freeze into DB first
	if _, err := st.Get(name); err != nil {
		if ctlErr != nil {
			return fmt.Errorf("preset %q not found and tmux unavailable: %v", name, ctlErr)
		}
		p, err := ctl.Freeze(name)
		if err != nil {
			return fmt.Errorf("freeze %q for edit: %w", name, err)
		}
		if _, _, err := template.FreezeSave(st, p, false); err != nil {
			return fmt.Errorf("save freeze for edit: %w", err)
		}
	}
	if err := template.Edit(st, name, picker.Pick); err != nil {
		return fmt.Errorf("edit %q: %w", name, err)
	}
	return nil
}
