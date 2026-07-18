package main

import (
	"errors"
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

// errCancel is user cancel (Esc / Ctrl+C in picker). Exit 0 — not a failure.
var errCancel = errors.New("canceled")

func main() {
	err := dispatch()
	if err == nil || errors.Is(err, errCancel) {
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func dispatch() error {
	if len(os.Args) < 2 {
		return runPicker()
	}
	switch os.Args[1] {
	case "-f", "--freeze":
		name := ""
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		return freezeCLI(name)
	case "-e", "--edit":
		name := ""
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		return editCLI(name)
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
  esc / ctrl-c  cancel (exit 0)

Store: $XDG_DATA_HOME/gotomux/state.db  (presets, shapes, sticky, usage)
	Layouts: $XDG_CONFIG_HOME/gotomux/layouts/<id>.json (1-1 backup)
Edit format: JSON {name,cwd,windows:[{name,layout,panes:[{cwd,cmd}]}]}
`, version)
		return nil
	default:
		return fmt.Errorf("unknown flag %q (try -h)", os.Args[1])
	}
}

// runPicker: interactive phase owns SIGINT → cancel = errCancel (exit 0).
// After Enter, SIGINT is released before connect so a stuck attach can be killed.
func runPicker() error {
	ctl, err := tmux.New()
	if err != nil {
		return fmt.Errorf("tmux: %w", err)
	}
	st, err := store.Open()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
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

	// --- phase: picker owns SIGINT ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	p := tea.NewProgram(m, opts...)

	// SIGINT → same intent as Esc / ctrl+c key: quit without connect.
	sigDone := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			p.Quit()
		case <-sigDone:
		}
	}()

	final, runErr := p.Run()
	close(sigDone)
	signal.Stop(sigCh)
	// drain any pending SIGINT so disposition is clean for shell after exit
	drainSignals(sigCh)

	if fm, ok := final.(interface {
		Done() picker.Result
		FrameLines() int
	}); ok {
		if !alt {
			picker.ClearInline(fm.FrameLines())
		}
		// cancel intents → exit 0
		if runErr != nil {
			if errors.Is(runErr, tea.ErrInterrupted) {
				return errCancel
			}
			return runErr
		}
		res := fm.Done()
		switch res.Action {
		case picker.ActionConnect:
			// --- phase: connect — SIGINT back to default (user may kill stuck attach) ---
			return connectItem(ctl, st, res.Item)
		default:
			// ActionQuit / ActionNone = user cancel
			return errCancel
		}
	}
	if runErr != nil {
		if errors.Is(runErr, tea.ErrInterrupted) {
			return errCancel
		}
		return runErr
	}
	return errCancel
}

func drainSignals(ch <-chan os.Signal) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
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

// freezeCLI: pick/resolve name freely (cancel = exit 0); hold SIGINT only for ACID write.
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
		if err != nil {
			return err
		}
		if name == "" {
			return errCancel
		}
	}

	// --- ACID: freeze + save must not be half-killed by SIGINT spam ---
	stop := holdSIGINT()
	p, err := ctl.Freeze(name)
	if err != nil {
		stop()
		return fmt.Errorf("freeze %q: %w", name, err)
	}
	sid, created, err := template.FreezeSave(st, p, false)
	stop()
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
	// Editor owns TTY/signals while running; only hold SIGINT around setup/save.
	st, err := store.Open()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctl, ctlErr := tmux.New()
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

	if _, err := st.Get(name); err != nil {
		if ctlErr != nil {
			return fmt.Errorf("preset %q not found and tmux unavailable: %v", name, ctlErr)
		}
		stop := holdSIGINT()
		p, err := ctl.Freeze(name)
		if err != nil {
			stop()
			return fmt.Errorf("freeze %q for edit: %w", name, err)
		}
		if _, _, err := template.FreezeSave(st, p, false); err != nil {
			stop()
			return fmt.Errorf("save freeze for edit: %w", err)
		}
		stop()
	}
	// Editor phase: default SIGINT (user cancels inside nvim)
	if err := template.Edit(st, name, picker.Pick); err != nil {
		return fmt.Errorf("edit %q: %w", name, err)
	}
	return nil
}

// holdSIGINT prevents default terminate during a short critical section (freeze tx).
// Stop restores default when the section ends.
func holdSIGINT() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				// discard — critical section must finish ACID
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
		drainSignals(ch)
	}
}

