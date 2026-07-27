package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

func printUsage() {
	fmt.Println(`Usage: gotomux [flags]

Flags:
  -h, --help     Show this help
  -v, --version  Show version
  -f, --freeze   Freeze current or named session as a preset
  -e, --edit     Edit a named preset (or freeze-then-edit)
  -p, --profile  Profile cold-start performance`)
}

func main() {
	initEventBus()
	cfg := config.Load()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help":
			printUsage()
			return
		case "-v", "--version":
			fmt.Println(version)
			return
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
		case "-p", "--profile":
			if err := profileRun(cfg); err != nil {
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

func runPicker(cfg *config.Config) error {
	trace("start")
	sock := cfg.SocketPath()
	if conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond); err == nil {
		trace("daemon dialed")
		return runPickerIPC(cfg, conn)
	}
	trace("no daemon")
	// No daemon: serve this run locally and start one for the next. Without this,
	// "instant" depended on the user having run `make install-all` to enable the
	// systemd unit — on any other machine every invocation paid full cold start
	// forever.
	spawnDaemon()
	return runPickerStandalone(cfg)
}

// spawnDaemon starts the daemon without waiting for it.
//
// It asks systemd first when the unit is installed. Forking directly would create
// an instance systemd does not know about, which then holds the single-instance
// flock — so `systemctl restart gotomuxd` could never acquire it, and with
// Restart=on-failure the unit relaunched in a loop. That happened; the raw fork is
// now only the fallback for machines with no user unit.
//
// Racing invocations are harmless either way: the loser sees ErrAlreadyRunning and
// exits zero. Set GOTOMUX_NO_AUTOSTART=1 to opt out entirely.
func spawnDaemon() {
	if os.Getenv("GOTOMUX_NO_AUTOSTART") != "" {
		return
	}
	if startViaSystemd() {
		return
	}
	spawnDaemonDirect()
}

// startViaSystemd reports whether it handed the job to the user's service manager.
func startViaSystemd() bool {
	sctl, err := exec.LookPath("systemctl")
	if err != nil {
		return false
	}
	// Only if the unit actually exists; otherwise `start` fails and we would have
	// silently done nothing.
	if out, err := exec.Command(sctl, "--user", "list-unit-files", "gotomuxd.service").Output(); err != nil ||
		!strings.Contains(string(out), "gotomuxd.service") {
		return false
	}
	return exec.Command(sctl, "--user", "start", "--no-block", "gotomuxd.service").Run() == nil
}

func spawnDaemonDirect() {
	bin, err := exec.LookPath("gotomuxd")
	if err != nil {
		// Fall back to a sibling of this binary, which is how it is laid out both
		// in the package and in a local `make build-all` tree.
		self, serr := os.Executable()
		if serr != nil {
			return
		}
		cand := filepath.Join(filepath.Dir(self), "gotomuxd")
		if st, serr := os.Stat(cand); serr != nil || st.IsDir() {
			return
		}
		bin = cand
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer devNull.Close()

	cmd := exec.Command(bin)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	// Setsid detaches it from this process group so it survives our exit and does
	// not receive the terminal's signals.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return
	}
	// Release the child; never Wait, or this process would block on it.
	_ = cmd.Process.Release()
}

func runPickerIPC(cfg *config.Config, conn net.Conn) error {
	defer conn.Close()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	enc.Encode(daemon.Request{Cmd: "list", SessID: tmux.CurrentSessionID()})
	var resp daemon.Response
	if err := decodeWithTimeout(dec, &resp); err != nil || !usablePayload(cfg, resp) {
		trace("payload unusable")
		return runPickerStandalone(cfg)
	}
	trace("payload decoded")

	cwd, _ := os.Getwd()
	root := project.FindProjectRoot(cwd)
	name := project.SessionName(root)

	ctl, _ := tmux.New()

	// The store stays closed. Everything the list needs is in the payload, so
	// opening SQLite here would put an open + migration probes + WAL setup on cold
	// start for data already in hand. lazyStore hands one to the action keys and
	// to the connect branches, which are the only things that need it.
	st := lazyStore(cfg)
	defer st.close()

	// Git branches come from the daemon so the picker does no filesystem walk.
	// Misses are seeded too: the daemon reports only real branches, and without
	// marking the rest as "not a repo" every non-repository row would be re-read
	// locally, reintroducing the very I/O the payload exists to avoid.
	picker.PreloadCache(resp.GitBranches)
	picker.PreloadMisses(payloadPaths(resp), resp.GitBranches)

	// Resolve our own session with zero forks: $TMUX carries the session index and
	// the payload carries #{session_id}. Previously this was two `tmux
	// display-message` forks on the path documented as doing no tmux I/O — and the
	// CtxSess/CtxPath fields the daemon sent were ignored, which was just as well:
	// they describe the daemon's process, which has no $TMUX and so always reported
	// nothing.
	env := picker.Context{Pairs: resp.Pairs, Usage: resp.Usage, Now: time.Now().Unix()}
	if cur, ok := tmux.FindByID(resp.Sessions, tmux.CurrentSessionID()); ok {
		env.Session, env.Path = cur.Name, cur.Path
	}

	seed := picker.Seed{
		Sessions:    resp.Sessions,
		Presets:     resp.Presets,
		ZoxideItems: picker.ZoxRowsToItems(resp.Zoxide),
		StickyLabel: resp.StickyLabel,
		Env:         &env,
	}
	deps := picker.Deps{Ctl: ctl, OpenStore: st.get}

	m := picker.NewModelFromDaemon(cfg, deps, name, root, seed)
	trace("model built (ipc)")
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

	// Record telemetry before connecting — ctl.Connect ends in syscall.Exec.
	// Prefer the daemon (it owns the store), but fall back to writing locally if
	// the ack does not come back, so a daemon that dies mid-session cannot
	// silently stop frecency from updating.
	if !recordOpenViaDaemon(enc, dec, it.Name) {
		recordOpen(st.get(), it.Name, liveNames(resp.Sessions))
	}

	ctx := context.Background()
	switch it.Kind {
	case picker.KindActive:
		// No store needed: attaching to a live session touches nothing on disk.
		return ctl.Connect(ctx, it.Name, "")
	case picker.KindPreset:
		p, e := st.get().Get(it.Name)
		if e != nil {
			return e
		}
		return ctl.ConnectPreset(ctx, p)
	default:
		return template.ConnectProject(ctl, st.get(), it.Name, it.Path)
	}
}

// storeHandle opens the store at most once, on first use.
type storeHandle struct {
	cfg  *config.Config
	once sync.Once
	st   *store.Store
}

// lazyStore defers store.OpenWithConfig until something actually needs it. On the
// daemon path that is never, for a plain browse-and-attach.
func lazyStore(cfg *config.Config) *storeHandle { return &storeHandle{cfg: cfg} }

func (h *storeHandle) get() store.Storer {
	h.once.Do(func() {
		st, err := store.OpenWithConfig(h.cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gotomux: store: %v\n", err)
			return
		}
		h.st = st
	})
	if h.st == nil {
		return nil
	}
	return h.st
}

func (h *storeHandle) close() {
	if h.st != nil {
		_ = h.st.Close()
	}
}

// recordOpenViaDaemon asks the daemon to record the open. Reports whether it
// acknowledged; a false return means the caller must record locally.
func recordOpenViaDaemon(enc *json.Encoder, dec *json.Decoder, name string) bool {
	if name == "" {
		return false
	}
	if enc.Encode(daemon.Request{Cmd: "connect", Name: name}) != nil {
		return false
	}
	var ack daemon.Response
	if decodeWithTimeout(dec, &ack) != nil {
		return false
	}
	return ack.OK
}

// usablePayload decides whether a daemon response may be trusted instead of
// gathering everything locally.
//
// OK alone is not enough: a daemon that has not completed a sync answers OK with
// an empty payload, and the picker cannot tell that apart from "you genuinely
// have no sessions and no presets". Requiring Ready plus a recent SyncedAt makes
// the degraded case representable, so the silent fallback actually fires.
func usablePayload(cfg *config.Config, resp daemon.Response) bool {
	if !resp.OK || !resp.Ready {
		return false
	}
	maxAge := 30 * time.Second
	if cfg != nil && cfg.PollInterval > 0 {
		maxAge = 3 * cfg.PollInterval
	}
	age := time.Since(time.Unix(resp.SyncedAt, 0))
	return age >= 0 && age <= maxAge
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
	trace("parallel init done")
	if ctlErr != nil {
		return fmt.Errorf("tmux: %w", ctlErr)
	}
	if stErr != nil {
		return fmt.Errorf("store: %w", stErr)
	}
	defer st.Close()
	name := project.SessionName(root)
	trace("model build (standalone)")
	return picker.RunPicker(cfg, ctl, st, name, root, func(res picker.Result) error {
		return connectItem(ctl, st, res)
	})
}

func profileRun(cfg *config.Config) error {
	fmt.Fprintln(os.Stderr, "profile:")
	totalStart := time.Now()

	log := func(name string, fn func()) {
		start := time.Now()
		fn()
		fmt.Fprintf(os.Stderr, "  %-28s %v\n", name, time.Since(start))
	}

	var (
		ctl    tmux.Connector
		st     *store.Store
		ctlErr error
		stErr  error
		root   string
	)
	log("parallel init", func() {
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
	})
	if ctlErr != nil {
		return fmt.Errorf("tmux: %w", ctlErr)
	}
	if stErr != nil {
		return fmt.Errorf("store: %w", stErr)
	}
	defer st.Close()

	var name string
	log("SessionName", func() {
		name = project.SessionName(root)
	})

	picker.ProfileRun(cfg, ctl, st, name, root)

	fmt.Fprintf(os.Stderr, "  %-28s %v\n", "total", time.Since(totalStart))
	return nil
}

func connectItem(ctl tmux.Connector, st store.Storer, res picker.Result) error {
	ctx := context.Background()
	it := res.Item
	recordOpen(st, it.Name, res.Live)
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

// recordOpen bumps frecency and co-occurrence for a session about to be opened.
//
// It must run BEFORE the connect: outside tmux, Ctl.Connect ends in syscall.Exec,
// which replaces this process — nothing after it runs, defers included.
//
// This is the standalone path's only telemetry write, and it did not exist. The
// comment in internal/tmux/ctl.go claimed the daemon's background poll handled
// it, but that poll depended on a control transport that never worked, so the
// usage table went unwritten and the recency tier of the ranking was frozen.
func recordOpen(st store.Storer, name string, live []string) {
	if st == nil || name == "" {
		return
	}
	_ = st.RecordOpen(name)
	_ = st.Touch(name) // no-op unless name is a saved preset
	others := make([]string, 0, len(live))
	for _, n := range live {
		if n != name {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		st.RecordPairsWithLive(name, others)
	}
}

// payloadPaths lists every path the payload mentions, matching what the daemon
// resolved git labels for.
func payloadPaths(resp daemon.Response) []string {
	out := make([]string, 0, len(resp.Sessions)+len(resp.Presets)+len(resp.Zoxide))
	for _, s := range resp.Sessions {
		out = append(out, s.Path)
	}
	for _, p := range resp.Presets {
		out = append(out, p.Cwd)
	}
	for _, z := range resp.Zoxide {
		out = append(out, z.Path)
	}
	return out
}

func liveNames(sessions []tmux.LiveSession) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Name)
	}
	return out
}

func freezeCLI(cfg *config.Config, name string) error {
	if handled, err := freezeViaDaemon(cfg, name); handled {
		return err
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

// freezeViaDaemon attempts the freeze over IPC.
//
// handled=false means "caller should run the standalone freeze" and is returned
// for every transport or daemon-side failure — IPC is an accelerator, never a
// dependency. Only a completed freeze, or a user cancellation (which must not
// re-prompt), counts as handled.
func freezeViaDaemon(cfg *config.Config, name string) (handled bool, err error) {
	conn, dialErr := net.DialTimeout("unix", cfg.SocketPath(), 50*time.Millisecond)
	if dialErr != nil {
		return false, nil
	}
	defer conn.Close()

	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	if enc.Encode(daemon.Request{Cmd: "list", SessID: tmux.CurrentSessionID()}) != nil {
		return false, nil
	}
	var listResp daemon.Response
	if decodeWithTimeout(dec, &listResp) != nil || !usablePayload(cfg, listResp) {
		return false, nil
	}

	if name == "" {
		// Resolve our own session from $TMUX + the payload's session ids; the daemon
		// cannot tell us, it has no $TMUX of its own.
		cur, inTmux := tmux.FindByID(listResp.Sessions, tmux.CurrentSessionID())
		switch {
		case inTmux:
			name = cur.Name
		case len(listResp.Sessions) > 0:
			items := make([]string, 0, len(listResp.Sessions))
			for _, s := range listResp.Sessions {
				items = append(items, s.Name)
			}
			picked, perr := picker.Pick(items)
			if perr != nil || picked == "" {
				return true, errCancel
			}
			name = picked
		}
	}
	if name == "" {
		return false, nil
	}

	if enc.Encode(daemon.Request{Cmd: "freeze", Name: name}) != nil {
		return false, nil
	}
	var fr daemon.Response
	if decodeWithTimeout(dec, &fr) != nil || !fr.OK {
		// Includes the daemon reporting tmux/store unavailable. Retrying locally
		// both has a chance of working and produces a real error message; the
		// old code returned fr.Error here, which for a dropped response was the
		// empty string — "freeze via daemon: " with no cause.
		return false, nil
	}
	fmt.Printf("froze %s\n", name)
	return true, nil
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
