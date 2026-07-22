package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/junegunn/fzf/src/algo"

	"github.com/fm39hz/gotomux/internal/daemon"
	"github.com/fm39hz/gotomux/internal/picker"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

var version = "dev"
var errCancel = picker.ErrCancel

func init() { algo.Init("default") }

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
		return freezeCLI(os.Args)
	case "-e", "--edit":
		return editCLI(os.Args)
	case "-h", "--help":
		showHelp()
		return nil
	default:
		return fmt.Errorf("unknown flag %q (try -h)", os.Args[1])
	}
}

func showHelp() {
	fmt.Printf(`gotomux - session picker (%s)

Usage:
  gotomux              interactive picker
  gotomux -f [name]    freeze session
  gotomux -e [name]    edit preset
  gotomux -h          show help
`, version)
}

func daemonSocket() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "gotomux", "gotomux.sock")
}

func runPicker() error {
	sock := daemonSocket()
	if conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond); err == nil {
		return runPickerIPC(conn)
	}
	return runPickerStandalone()
}

func runPickerIPC(conn net.Conn) error {
	defer conn.Close()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	enc.Encode(daemon.Request{Cmd: "list"})
	var resp daemon.Response
	if err := dec.Decode(&resp); err != nil || !resp.OK {
		return runPickerStandalone()
	}

	cwd, _ := os.Getwd()
	root := project.FindProjectRoot(cwd)
	name := project.SessionName(root)

	// Local ctl+store cho actions trong session.
	ctl, _ := tmux.New()
	st, stErr := store.Open()
	if stErr != nil {
		return fmt.Errorf("store: %w", stErr)
	}
	defer st.Close()

	env := picker.Context{
		Session: resp.CtxSess, Path: resp.CtxPath,
		Pairs: resp.Pairs, Usage: resp.Usage, Now: time.Now().Unix(),
	}

	m := picker.NewModelFromDaemon(ctl, st, name, root, resp.Sessions, resp.Presets, env)
	opts, _, err := picker.TeaOpts()
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, opts...)
	final, runErr := picker.RunCancellable(p)
	if runErr != nil {
		return runErr
	}
	fm, ok := final.(interface {
		Done() picker.Result
		FrameLines() int
	})
	if !ok {
		return errCancel
	}
	picker.ClearInline(fm.FrameLines())
	res := fm.Done()
	if res.Action != picker.ActionConnect {
		return errCancel
	}
	it := res.Item
	switch it.Kind {
	case picker.KindActive:
		return ctl.Connect(it.Name, "")
	case picker.KindPreset:
		p, e := st.Get(it.Name)
		if e != nil {
			return e
		}
		return ctl.ConnectPreset(p)
	default:
		return template.ConnectProject(ctl, st, it.Name, it.Path)
	}
}

func runPickerStandalone() error {
	var (
		ctl    tmux.Connector
		st     *store.Store
		ctlErr error
		stErr  error
		root   string
	)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		c, e := tmux.New()
		ctl, ctlErr = c, e
	}()
	go func() {
		defer wg.Done()
		s, e := store.Open()
		st, stErr = s, e
	}()
	go func() {
		defer wg.Done()
		cwd, _ := os.Getwd()
		root = project.FindProjectRoot(cwd)
	}()
	wg.Wait()
	if ctlErr != nil {
		return fmt.Errorf("tmux: %w", ctlErr)
	}
	if stErr != nil {
		return fmt.Errorf("store: %w", stErr)
	}
	defer st.Close()
	name := project.SessionName(root)
	return picker.RunPicker(ctl, st, name, root, func(it picker.Item) error {
		return connectItem(ctl, st, it)
	})
}

func connectItem(ctl tmux.Connector, st *store.Store, it picker.Item) error {
	switch it.Kind {
	case picker.KindCreate, picker.KindZoxide:
		return template.ConnectProject(ctl, st, it.Name, it.Path)
	case picker.KindActive:
		return ctl.Connect(it.Name, "")
	case picker.KindPreset:
		if st == nil {
			return fmt.Errorf("connect preset: nil store")
		}
		p, e := st.Get(it.Name)
		if e != nil {
			return e
		}
		return ctl.ConnectPreset(p)
	default:
		return fmt.Errorf("unknown kind %v", it.Kind)
	}
}

func freezeCLI(args []string) error {
	name := ""
	if len(args) > 2 {
		name = args[2]
	}
	ctl, err := tmux.New()
	if err != nil {
		return fmt.Errorf("tmux: %w", err)
	}
	st, err := store.Open()
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()
	if name == "" {
		name = ctl.CurrentSession()
	}
	if name == "" {
		live, e := ctl.ListLive()
		if e != nil {
			return e
		}
		if len(live) == 0 {
			return fmt.Errorf("no active sessions")
		}
		items := make([]string, 0, len(live))
		for _, s := range live {
			items = append(items, s.Name)
		}
		_ = st
		_ = live
		name, err = picker.Pick(items)
		if err != nil || name == "" {
			return errCancel
		}
	}
	stop := picker.HoldInterrupt()
	_, _, err = template.FreezeRemember(ctl, st, name)
	stop()
	if err != nil {
		return err
	}
	fmt.Printf("froze %s\n", name)
	return nil
}

func editCLI(args []string) error {
	name := ""
	if len(args) > 2 {
		name = args[2]
	}
	st, err := store.Open()
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()
	ctl, ctlErr := tmux.New()
	if name == "" && ctlErr == nil && ctl != nil {
		name = ctl.CurrentSession()
	}
	if name == "" {
		return template.Edit(st, "", picker.Pick)
	}
	if _, err := st.Get(name); err != nil {
		if ctlErr != nil {
			return fmt.Errorf("preset %q not found and tmux unavailable: %v", name, ctlErr)
		}
		stop := picker.HoldInterrupt()
		_, _, err = template.FreezeRemember(ctl, st, name)
		stop()
		if err != nil {
			return err
		}
	}
	return template.Edit(st, name, picker.Pick)
}
