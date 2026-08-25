package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

// newIsolated builds a daemon against a private tmux server and a private data
// dir, so nothing here can reach the developer's real server, store, socket or
// lock file. tmuxtest.Isolate proves the isolation before any teardown runs.
func newIsolated(t *testing.T, sessions ...string) *Daemon {
	t.Helper()

	root := tmuxtest.Isolate(t)

	// XDG_DATA_HOME keeps the daemon's store, socket and lock file inside the
	// scratch dir; a fresh empty XDG_CONFIG_HOME keeps the developer's real
	// config.toml out of the daemon's settings.
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", state, err)
	}
	t.Setenv("XDG_DATA_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_RUNTIME_DIR", state)

	tmuxtest.NewSessions(t, sessions...)

	d, err := New(config.Load())
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func TestDaemonServesCompletePayload(t *testing.T) {
	d := newIsolated(t, "dt-alpha", "dt-beta")

	resp := d.buildListResponse("")

	// Ready is the whole point: an OK response with an empty payload used to be
	// indistinguishable from success, which is how a daemon whose tmux transport
	// never worked served nothing for its entire life without anyone noticing.
	if !resp.Ready {
		t.Fatalf("Ready=false — payload incomplete; sessions=%d presets=%d",
			len(resp.Sessions), len(resp.Presets))
	}
	if resp.SyncedAt == 0 {
		t.Error("SyncedAt=0 on a ready payload")
	}
	if age := time.Since(time.Unix(resp.SyncedAt, 0)); age > 30*time.Second {
		t.Errorf("SyncedAt is %v old on a fresh daemon", age)
	}

	names := sessionNames(resp.Sessions)
	for _, want := range []string{"dt-alpha", "dt-beta"} {
		if !names[want] {
			t.Errorf("session %q missing from payload (%v)", want, keys(names))
		}
	}
	if names[tmux.HiddenControlSession] {
		t.Errorf("hidden control session leaked into the payload (%v)", keys(names))
	}
	for _, s := range resp.Sessions {
		if s.Path == "" {
			t.Errorf("session %q has empty Path — list format was not quoted", s.Name)
		}
	}
}

func TestDaemonSyncsOnEventNotOnlyOnPoll(t *testing.T) {
	// PollInterval is deliberately long: if the session shows up quickly it can
	// only have come from the control-mode event path.
	t.Setenv("GOTOMUX_POLL_INTERVAL", "10m")
	d := newIsolated(t, "dt-base")

	tmuxtest.NewSessions(t, "dt-late")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sessionNames(d.buildListResponse("").Sessions)["dt-late"] {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session created externally did not appear within 5s; event-driven resync is not working")
}

func TestDaemonServesMultipleRequestsPerConnection(t *testing.T) {
	d := newIsolated(t, "dt-conn")

	go func() { _ = ServeIPC(d) }()

	conn := dialDaemon(t, d)
	defer conn.Close()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)

	// Request 1: list.
	if err := enc.Encode(Request{Cmd: "list"}); err != nil {
		t.Fatalf("encode list: %v", err)
	}
	var first Response
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if !first.Ready {
		t.Fatalf("list not ready")
	}

	// Request 2 on the SAME connection. The handler used to decode exactly one
	// request and close, so this silently vanished — which is what killed all
	// open/pair telemetry and made `gotomux -f` fail with an empty error.
	if err := enc.Encode(Request{Cmd: "ping"}); err != nil {
		t.Fatalf("encode second request: %v", err)
	}
	var second Response
	if err := dec.Decode(&second); err != nil {
		t.Fatalf("second request on the same connection was dropped: %v", err)
	}
	if !second.OK {
		t.Errorf("second response not OK: %+v", second)
	}

	// Request 3: unknown command must answer, not hang until the client's
	// decode timeout.
	if err := enc.Encode(Request{Cmd: "definitely-not-a-command"}); err != nil {
		t.Fatalf("encode unknown: %v", err)
	}
	var third Response
	if err := dec.Decode(&third); err != nil {
		t.Fatalf("unknown command got no response: %v", err)
	}
	if third.OK || !strings.Contains(third.Error, "unknown cmd") {
		t.Errorf("unknown command response = %+v, want OK=false with an error", third)
	}
}

func TestDaemonRecordsConnectTelemetry(t *testing.T) {
	d := newIsolated(t, "dt-tel")

	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()
	if st == nil {
		t.Fatal("no store")
	}

	before, _ := st.AllUsage()
	d.handleConnect("dt-tel")
	after, _ := st.AllUsage()

	if after["dt-tel"].Opens <= before["dt-tel"].Opens {
		t.Errorf("opens did not increase: before=%d after=%d",
			before["dt-tel"].Opens, after["dt-tel"].Opens)
	}
}

func dialDaemon(t *testing.T, d *Daemon) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", d.sockPath, 200*time.Millisecond); err == nil {
			return conn
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("daemon socket %s never became connectable", d.sockPath)
	return nil
}

func sessionNames(in []tmux.LiveSession) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s.Name] = true
	}
	return m
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDaemonDoesNotCreateTmuxServer is the regression guard for a data-loss bug.
//
// The daemon used to run `tmux start-server` from inside its own process. tmux
// registers a systemd transient scope per pane, parented under the unit that
// started the server — so on any machine where gotomuxd started before the user's
// first tmux, every pane of every session became a child of gotomuxd.service, and
// `systemctl stop gotomuxd` destroyed all of them. Verified on a real machine: the
// journal logged "Stopping tmux child pane N" per pane and the server was left
// with zero sessions. KillMode=process spares the server process but not the pane
// scopes.
//
// So: starting a daemon with no tmux server running must leave there being no tmux
// server running.
func TestDaemonDoesNotCreateTmuxServer(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: needs a tmux binary")
	}
	root := tmuxtest.Isolate(t)

	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_RUNTIME_DIR", state)

	// Isolate() starts a server so it can prove isolation; kill it so we begin
	// from "no server at all".
	if err := exec.Command("tmux", "kill-server").Run(); err != nil {
		t.Fatalf("kill-server: %v", err)
	}
	if tmux.ServerRunning() {
		t.Fatal("server still running after kill-server")
	}

	d, err := New(config.Load())
	if err != nil {
		t.Fatalf("daemon.New with no tmux server: %v (it must start degraded, not fail)", err)
	}
	t.Cleanup(d.Close)

	if tmux.ServerRunning() {
		t.Error("daemon started a tmux server; every pane created in it would be scoped under the daemon's unit")
	}

	// With no server there is nothing to observe, so the payload must declare
	// itself unusable rather than assert an empty session list is the truth.
	if resp := d.buildListResponse(""); resp.Ready {
		t.Error("Ready=true with no tmux server; clients would trust an empty session list")
	}
}

// TestDaemonAttachesToAnExistingServer: the flip side — when a server appears, the
// daemon must pick it up without being restarted.
func TestDaemonAttachesToAnExistingServer(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: needs a tmux binary")
	}
	root := tmuxtest.Isolate(t)
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_RUNTIME_DIR", state)

	if err := exec.Command("tmux", "kill-server").Run(); err != nil {
		t.Fatalf("kill-server: %v", err)
	}
	cfg := config.Load()
	cfg.PollInterval = time.Second // default 10s would outrun this test's deadline
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	t.Cleanup(d.Close)
	if d.buildListResponse("").Ready {
		t.Fatal("Ready before any server existed")
	}

	// Someone starts tmux the normal way.
	if err := exec.Command("tmux", "new-session", "-d", "-s", "dt-appeared").Run(); err != nil {
		t.Fatalf("new-session: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := d.buildListResponse("")
		if resp.Ready && sessionNames(resp.Sessions)["dt-appeared"] {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("daemon never attached to the server that appeared after it started")
}

// TestPrewarmPathsAreRealFiles: the list must contain only existing regular
// files, deduped through symlinks — a bad entry would just waste a syscall, but a
// duplicated 9.7 MB binary would double the I/O this is meant to minimise.
func TestPrewarmPathsAreRealFiles(t *testing.T) {
	dir := t.TempDir()
	paths := prewarmPaths(&config.Config{DataDir: dir})

	seen := map[string]bool{}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("prewarm path %q does not exist", p)
			continue
		}
		if fi.IsDir() {
			t.Errorf("prewarm path %q is a directory", p)
		}
		if seen[p] {
			t.Errorf("duplicate prewarm path %q", p)
		}
		seen[p] = true
	}
}

func TestPrewarmRespectsOptOut(t *testing.T) {
	// Must return promptly without touching anything; the assertion is that it does
	// not panic or block.
	done := make(chan struct{})
	go func() { prewarm(&config.Config{Prewarm: "off"}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(`prewarm ignored prewarm = "off"`)
	}
}

// TestAlreadyRunningIsNotAFailure: a second instance must report
// ErrAlreadyRunning so main can exit zero. Returning a plain error made systemd
// (Restart=on-failure) relaunch the unit forever against a lock held by an
// autostarted instance — observed at restart counter 8.
func TestAlreadyRunningIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	sock := filepath.Join(dir, "gotomux.sock")
	unlock, err := acquireLock(sock)
	if err != nil {
		t.Fatalf("first acquireLock: %v", err)
	}
	defer unlock()

	// Same process cannot re-take its own flock via a second fd on Linux? It can,
	// so assert the classification path instead: the error a second holder gets
	// must be recognisable.
	if _, err := acquireLock(sock); err != nil && !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second acquireLock error = %v; must wrap ErrAlreadyRunning", err)
	}

	// And a live listener must produce the same classification.
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	if _, err := listenWithGuard(sock); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("listenWithGuard against a live socket = %v; must wrap ErrAlreadyRunning", err)
	}
}

func TestAttachTransition(t *testing.T) {
	cases := []struct {
		name     string
		prev     map[string]int
		cur      map[string]int
		wantFrom string
		wantTo   string
		wantOK   bool
	}{
		{name: "classic switch", prev: map[string]int{"a": 1}, cur: map[string]int{"b": 1}, wantFrom: "a", wantTo: "b", wantOK: true},
		{name: "first poll baseline", prev: map[string]int{}, cur: map[string]int{"a": 1}},
		{name: "detach to outside", prev: map[string]int{"a": 1}, cur: map[string]int{}},
		{name: "attach from outside", prev: map[string]int{}, cur: map[string]int{"a": 1}},
		{name: "two leave one joins", prev: map[string]int{"a": 1, "b": 1}, cur: map[string]int{"c": 1}},
		{name: "one leaves two join", prev: map[string]int{"a": 1}, cur: map[string]int{"b": 1, "c": 1}},
		{name: "attach creates no switch", prev: map[string]int{"a": 1}, cur: map[string]int{"a": 1, "b": 1}},
		{name: "same set", prev: map[string]int{"a": 1}, cur: map[string]int{"a": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to, ok := attachTransition(tc.prev, tc.cur)
			if ok != tc.wantOK || from != tc.wantFrom || to != tc.wantTo {
				t.Errorf("attachTransition = %q -> %q ok=%v; want %q -> %q ok=%v",
					from, to, ok, tc.wantFrom, tc.wantTo, tc.wantOK)
			}
		})
	}
}

// TestDaemonDiffTelemetryRecordsTransitions drives diffTelemetry with synthetic
// session lists and asserts the directed switch lands in the store exactly once,
// never involving the daemon's own hidden control session.
func TestDaemonDiffTelemetryRecordsTransitions(t *testing.T) {
	d := newIsolated(t, "dt-trx")
	hidden := tmux.HiddenControlSession

	sess := func(name string, attached int) tmux.LiveSession {
		return tmux.LiveSession{ID: "$x", Name: name, LastAttached: 1, Attached: attached}
	}

	// Baseline: no prior attached set, so the first poll must not invent a
	// transition into whatever happens to be attached.
	d.diffTelemetry([]tmux.LiveSession{sess("dt-a", 1), sess("dt-b", 0), sess(hidden, 1)})

	// Switch dt-a -> dt-b; hidden stays attached the whole time.
	d.diffTelemetry([]tmux.LiveSession{sess("dt-a", 0), sess("dt-b", 1), sess(hidden, 1)})

	// Detach-only: the client left tmux altogether, nothing to learn.
	d.diffTelemetry([]tmux.LiveSession{sess("dt-a", 0), sess("dt-b", 0), sess(hidden, 1)})

	// Attach-only: came from outside tmux, no prev session exists.
	d.diffTelemetry([]tmux.LiveSession{sess("dt-c", 1), sess(hidden, 1)})

	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()
	if st == nil {
		t.Fatal("no store")
	}
	now := time.Now().Unix()
	fromA, err := st.TransitionScores("dt-a", now)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	if fromA["dt-b"] <= 0 {
		t.Errorf("dt-a->dt-b not recorded: %+v", fromA)
	}
	if len(fromA) != 1 {
		t.Errorf("dt-a transitions = %+v, want exactly dt-b", fromA)
	}
	// No transition from or to the hidden control session.
	fromHidden, err := st.TransitionScores(hidden, now)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	if len(fromHidden) != 0 {
		t.Errorf("transitions from hidden session: %+v", fromHidden)
	}
	// Detach-only and attach-only steps must not have produced rows: dt-b was
	// never a prev, and dt-c never had a prev.
	fromB, err := st.TransitionScores("dt-b", now)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	fromC, err := st.TransitionScores("dt-c", now)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	if len(fromB) != 0 || len(fromC) != 0 {
		t.Errorf("unexpected transitions: fromB=%+v fromC=%+v", fromB, fromC)
	}
}

// ---- tmux-free harness for the P1.1/P2.3/P2.4 unit tests ----
//
// newIsolated exercises the full daemon against a real (isolated) tmux server,
// which is why those tests skip under -short. The features added here are
// unit-level, so they run against newBare instead: a stubbed control client
// whose Alive is pinned true, meaning no code path under test can ever probe —
// let alone attach to — the developer's real tmux server.

// stubCC satisfies controlClient without spawning tmux. Alive pinned true keeps
// ensureControl and listLiveViaControl off the real environment; send lets a
// test inject behavior (canned output, a panic) per control-mode exchange.
type stubCC struct {
	events chan string
	send   func(ctx context.Context, cmds ...[]string) (string, error)
}

func newStubCC() *stubCC { return &stubCC{events: make(chan string, 8)} }

func (s *stubCC) Alive() bool           { return true }
func (s *stubCC) Reconnect() error      { return nil }
func (s *stubCC) Events() <-chan string { return s.events }
func (s *stubCC) Timeouts() int64       { return 0 }
func (s *stubCC) Close()                {}

func (s *stubCC) SendLines(ctx context.Context, cmds ...[]string) (string, error) {
	if s.send != nil {
		return s.send(ctx, cmds...)
	}
	return "", nil
}

// newBare builds a minimal daemon against a private temp data dir: stubbed
// control client, a real store inside the temp dir, and no background
// goroutines — each test starts exactly the loops it exercises.
func newBare(t *testing.T, tune func(*config.Config)) *Daemon {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{DataDir: root, PollInterval: 10 * time.Second}
	if tune != nil {
		tune(cfg)
	}
	dir := cfg.ResolveDataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	st, err := store.OpenWithConfig(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// acquireLock keys off XDG_RUNTIME_DIR; point it at the scratch tree so
	// ServeIPC never touches the developer's real lock file.
	t.Setenv("XDG_RUNTIME_DIR", root)

	d := &Daemon{
		cc: newStubCC(), st: st,
		stPath:   filepath.Join(dir, "state.db"),
		lastSeen: map[string]int64{}, lastAttached: map[string]int{},
		sockPath: filepath.Join(dir, "test.sock"),
		stopCh:   make(chan struct{}), startedAt: time.Now(),
	}
	d.cfgPtr.Store(cfg)
	t.Cleanup(func() { d.Close() })
	return d
}

// armPanicHook arms the syncNowInner seam and returns an explicit disarm.
//
// Disarm MUST be called in the test body after every loop that ticks has been
// joined (d.Close()), because disarming while a loop is mid-tick is a data race
// the detector will flag. There is deliberately no automatic cleanup: cleanup
// functions run in LIFO order, which would put the disarm before newBare's
// d.Close and reintroduce exactly that race whenever a test forgot to join.
func armPanicHook(t *testing.T, f func()) func() {
	t.Helper()
	panicHook.Store(&f)
	called := false
	return func() {
		if !called {
			called = true
			panicHook.Store(nil)
		}
	}
}

// TestSyncNowIsSingleFlight pins the actual invariant: however many goroutines
// fire syncNow simultaneously, at most ONE body of syncNowInner is executing at
// any moment. diffTelemetry's read-modify-write over lastSeen/lastAttached is
// only correct under that property; the mutex alone cannot give it, because two
// serialized-by-mutex-but-both-started-from-the-same-snapshot runs would still
// double-record.
func TestSyncNowIsSingleFlight(t *testing.T) {
	d := newBare(t, nil)
	var active, maxActive atomic.Int64
	disarm := armPanicHook(t, func() {
		cur := active.Add(1)
		for {
			m := maxActive.Load()
			if cur <= m || maxActive.CompareAndSwap(m, cur) {
				break
			}
		}
		// Widen the overlap window so any guard breach is virtually certain to
		// be observed rather than squeezed between two instructions.
		time.Sleep(50 * time.Microsecond)
		active.Add(-1)
	})
	defer disarm()

	const workers, loops = 8, 250
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < loops; j++ {
				d.syncNow()
			}
		}()
	}
	wg.Wait()

	if n := maxActive.Load(); n != 1 {
		t.Errorf("%d syncNow bodies ran concurrently; the single-flight guard is broken", n)
	}
}

// TestSyncNowDropsWhileInFlight shows the drop is immediate: callers arriving
// while a sync holds the flag RETURN, they are neither queued nor blocked.
func TestSyncNowDropsWhileInFlight(t *testing.T) {
	d := newBare(t, nil)

	release := make(chan struct{})
	firstIn := make(chan struct{})
	armPanicHook(t, func() {
		select {
		case firstIn <- struct{}{}:
		default:
		}
		<-release
	})

	holderDone := make(chan struct{})
	go func() { d.syncNow(); close(holderDone) }()
	select {
	case <-firstIn:
	case <-time.After(2 * time.Second):
		t.Fatal("first syncNow never reached the body")
	}

	const drops = 16
	dropped := make(chan struct{}, drops)
	for i := 0; i < drops; i++ {
		go func() {
			d.syncNow()
			dropped <- struct{}{}
		}()
	}
	for i := 0; i < drops; i++ {
		select {
		case <-dropped:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/%d concurrent callers returned while a sync was in flight; they are waiting instead of dropping", i, drops)
		}
	}
	select {
	case <-holderDone:
		t.Fatal("holder finished while release was still closed; the hook did not hold it")
	default:
	}

	close(release)
	<-holderDone
}

// TestPollLoopSurvivesPanicAndKeepsServing drives the whole containment story:
// every tick panics via the injected hook, yet the loop keeps ticking (the tick
// counter advances past multiple recovered panics), evidence lands in
// <DataDir>/crash_<unix>.log, and the IPC surface keeps answering throughout.
func TestPollLoopSurvivesPanicAndKeepsServing(t *testing.T) {
	d := newBare(t, func(c *config.Config) { c.PollInterval = 20 * time.Millisecond })

	var ticks atomic.Int64
	disarm := armPanicHook(t, func() {
		ticks.Add(1)
		panic("boom-test-panic")
	})

	go func() { _ = ServeIPC(d) }()
	d.wg.Add(1) // mirror New: pollLoop owns one wg slot
	go d.pollLoop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ticks.Load() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if ticks.Load() < 3 {
		disarm()
		d.Close()
		t.Fatalf("tick body entered %d times; the poll loop did not survive repeated panics", ticks.Load())
	}

	// While the panic storm is ongoing, a client must still get answers.
	conn := dialDaemon(t, d)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	if err := enc.Encode(Request{Cmd: "ping"}); err != nil {
		t.Fatalf("encode ping: %v", err)
	}
	var pong Response
	if err := dec.Decode(&pong); err != nil {
		t.Fatalf("decode ping response: %v", err)
	}
	if !pong.OK {
		t.Errorf("ping not OK while the poll loop is recovering panics: %+v", pong)
	}

	// Join the loops BEFORE disarming (see armPanicHook) and before reading the
	// crash log, so the file is final rather than mid-overwrite.
	disarm()
	d.Close()

	logs, err := filepath.Glob(filepath.Join(d.conf().ResolveDataDir(), "crash_*.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no crash log written (err=%v logs=%v)", err, logs)
	}
	body, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatalf("read crash log: %v", err)
	}
	for _, want := range []string{"boom-test-panic", "goroutine ", runtime.GOOS, runtime.GOARCH} {
		if !strings.Contains(string(body), want) {
			t.Errorf("crash log missing %q\n---\n%s\n---", want, body)
		}
	}
}

// TestReloadConfigChangesPollInterval: a reload must reach a RUNNING loop
// without restarting anything — the very next tick picks up the new cadence.
func TestReloadConfigChangesPollInterval(t *testing.T) {
	d := newBare(t, func(c *config.Config) { c.PollInterval = 20 * time.Millisecond })

	var ticks atomic.Int64
	disarm := armPanicHook(t, func() { ticks.Add(1) })
	defer disarm()

	d.wg.Add(1) // mirror New: pollLoop owns one wg slot
	go d.pollLoop()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ticks.Load() < 5 {
		time.Sleep(10 * time.Millisecond)
	}
	if ticks.Load() < 5 {
		d.Close()
		t.Fatalf("loop never reached the initial fast cadence (ticks=%d)", ticks.Load())
	}

	slow := &config.Config{DataDir: d.conf().DataDir, PollInterval: time.Hour}
	d.ReloadConfig(slow)
	if got := d.conf(); got != slow {
		d.Close()
		t.Fatal("active config pointer was not swapped by ReloadConfig")
	}

	before := ticks.Load()
	time.Sleep(500 * time.Millisecond)
	// One already-queued tick may fire right after Reset; five would mean the
	// ticker kept the old cadence (~25 ticks would have landed in 500ms).
	if growth := ticks.Load() - before; growth > 5 {
		d.Close()
		t.Errorf("%d ticks fired in 500ms after reloading to a 1h interval; pollLoop is not re-reading the config", growth)
	}

	// A nil config (failed load upstream) must not blank out running settings.
	d.ReloadConfig(nil)
	if d.conf() != slow {
		t.Error("ReloadConfig(nil) replaced the active config")
	}

	d.Close()
}

// TestHandleConnRecoversPanicsPerRequest: a request whose handling panics must
// come back to THAT client as an error Response and leave the connection (and
// the accept loop) fully serviceable for the next request.
func TestHandleConnRecoversPanicsPerRequest(t *testing.T) {
	d := newBare(t, nil)
	d.cc.(*stubCC).send = func(ctx context.Context, cmds ...[]string) (string, error) {
		panic("list exploded")
	}

	go func() { _ = ServeIPC(d) }()

	conn := dialDaemon(t, d)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)

	// connect walks into the (stubbed) control client, whose send now panics.
	if err := enc.Encode(Request{Cmd: "connect", Name: "dt-poison"}); err != nil {
		t.Fatalf("encode poisoned request: %v", err)
	}
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("poisoned request got no response: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "internal error") {
		t.Errorf("panicked request answered %+v, want OK=false with an internal error", resp)
	}

	// The SAME connection must keep serving: containment is per-request.
	if err := enc.Encode(Request{Cmd: "ping"}); err != nil {
		t.Fatalf("encode ping after panic: %v", err)
	}
	var pong Response
	if err := dec.Decode(&pong); err != nil {
		t.Fatalf("connection died after a recovered request panic: %v", err)
	}
	if !pong.OK {
		t.Errorf("ping after recovered panic not OK: %+v", pong)
	}
}
