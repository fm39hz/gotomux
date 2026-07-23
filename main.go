package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/junegunn/fzf/src/algo"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/daemon"
	"github.com/fm39hz/gotomux/internal/event"
	"github.com/fm39hz/gotomux/internal/picker"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

var version = "dev"
var errCancel = picker.ErrCancel

func init() { algo.Init("default") }

type cli struct {
	Version bool `short:"v" help:"Show version"`
}

func main() {
	initEventBus()
	cfg := config.Load()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-f", "--freeze":
			name := ""
			if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
				name = os.Args[2]
			}
			if err := freezeCLI(cfg, name); err != nil && !errors.Is(err, errCancel) {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "-e", "--edit":
			name := ""
			if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
				name = os.Args[2]
			}
			if err := editCLI(cfg, name); err != nil && !errors.Is(err, errCancel) {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	if err := runPicker(cfg); err != nil && !errors.Is(err, errCancel) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func initEventBus() {
	template.SetEventBus(event.New())
}

func daemonStateFile() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "gotomux", "state.ver")
}

func daemonSocket() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "gotomux", "gotomux.sock")
}

func runPicker(cfg *config.Config) error {
	sock := daemonSocket()
	if conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond); err == nil {
		return runPickerIPC(cfg, conn)
	}
	return runPickerStandalone(cfg)
}

func runPickerIPC(cfg *config.Config, conn net.Conn) error {
	defer conn.Close()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	enc.Encode(daemon.Request{Cmd: "list"})
	var resp daemon.Response
	if err := decodeWithTimeout(dec, &resp); err != nil || !resp.OK {
		return runPickerStandalone(cfg)
	}

	cwd, _ := os.Getwd()
	root := project.FindProjectRoot(cwd)
	name := project.SessionName(root)

	ctl, _ := tmux.New()
	st, stErr := store.OpenWithConfig(cfg)
	if stErr != nil {
		return fmt.Errorf("store: %w", stErr)
	}
	defer st.Close()

	ctx := context.Background()
	env := picker.Context{
		Session: ctl.CurrentSession(ctx), Path: ctl.CurrentSessionPath(ctx),
		Pairs: resp.Pairs, Usage: resp.Usage, Now: time.Now().Unix(),
	}

	m := picker.NewModelFromDaemon(cfg, ctl, st, name, root, resp.Sessions, resp.Presets, env)
	opts, _, err := picker.TeaOpts()
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, opts...)
	final, runErr := picker.RunCancellable(p)
	if runErr != nil {
		return runErr
	}

	fm, ok := final.(interface{ Done() picker.Result })
	if !ok {
		return errCancel
	}

	res := fm.Done()
	if res.Action != picker.ActionConnect {
		return errCancel
	}
	it := res.Item

	enc.Encode(daemon.Request{Cmd: "connect", Name: it.Name})
	var ack daemon.Response
	decodeWithTimeout(dec, &ack)

	switch it.Kind {
	case picker.KindActive:
		return ctl.Connect(ctx, it.Name, "")
	case picker.KindPreset:
		p, e := st.Get(it.Name)
		if e != nil {
			return e
		}
		return ctl.ConnectPreset(ctx, p)
	default:
		return template.ConnectProject(ctl, st, it.Name, it.Path)
	}
}

// decodeWithTimeout reads one JSON value with a 2-second timeout.
// On timeout or error, returns an error so callers fall back to standalone.
func decodeWithTimeout(dec *json.Decoder, v any) error {
	type res struct{ err error }
	ch := make(chan res, 1)
	go func() {
		ch <- res{dec.Decode(v)}
	}()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(2 * time.Second):
		return fmt.Errorf("IPC response timeout")
	}
}

func runPickerStandalone(cfg *config.Config) error {
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
		s, e := store.OpenWithConfig(cfg)
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
	return picker.RunPicker(cfg, ctl, st, name, root, func(it picker.Item) error {
		return connectItem(ctl, st, it)
	})
}

func connectItem(ctl tmux.Connector, st store.Storer, it picker.Item) error {
	ctx := context.Background()
	switch it.Kind {
	case picker.KindCreate, picker.KindZoxide:
		return template.ConnectProject(ctl, st, it.Name, it.Path)
	case picker.KindActive:
		return ctl.Connect(ctx, it.Name, "")
	case picker.KindPreset:
		if st == nil {
			return fmt.Errorf("connect preset: nil store")
		}
		p, e := st.Get(it.Name)
		if e != nil {
			return e
		}
		return ctl.ConnectPreset(ctx, p)
	default:
		return fmt.Errorf("unknown kind %v", it.Kind)
	}
}

func freezeCLI(cfg *config.Config, name string) error {
	if conn, err := net.DialTimeout("unix", daemonSocket(), 50*time.Millisecond); err == nil {
		defer conn.Close()
		enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
		enc.Encode(daemon.Request{Cmd: "list"})
		var listResp daemon.Response
		if err := decodeWithTimeout(dec, &listResp); err == nil && listResp.OK {
			if name == "" {
				if listResp.CtxSess != "" {
					name = listResp.CtxSess
				} else if len(listResp.Sessions) > 0 {
					items := make([]string, 0, len(listResp.Sessions))
					for _, s := range listResp.Sessions {
						items = append(items, s.Name)
					}
					name, err = picker.Pick(items)
					if err != nil || name == "" {
						return errCancel
					}
				}
			}
			if name != "" {
				enc.Encode(daemon.Request{Cmd: "freeze", Name: name})
				var fr daemon.Response
				decodeWithTimeout(dec, &fr)
				if fr.OK {
					fmt.Printf("froze %s\n", name)
					return nil
				}
				return fmt.Errorf("freeze via daemon: %s", fr.Error)
			}
		}
	}

	// Fallback: standalone freeze
	ctl, err := tmux.New()
	if err != nil {
		return fmt.Errorf("tmux: %w", err)
	}
	st, err := store.OpenWithConfig(cfg)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()
	if name == "" {
		name = ctl.CurrentSession(context.Background())
	}
	if name == "" {
		live, e := ctl.ListLive(context.Background())
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

func editCLI(cfg *config.Config, name string) error {
	st, err := store.OpenWithConfig(cfg)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()
	ctl, ctlErr := tmux.New()
	if name == "" && ctlErr == nil && ctl != nil {
		name = ctl.CurrentSession(context.Background())
	}
	if name == "" {
		return template.Edit(st, "", picker.Pick)
	}
	if _, err := st.Get(name); err != nil {
		if ctlErr != nil {
			return fmt.Errorf("preset %q not found and tmux unavailable: %w", name, ctlErr)
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
