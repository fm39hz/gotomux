package daemon

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

// newIsolated builds a daemon against a private tmux server and a private data
// dir, so nothing here can reach the developer's real server, store, socket or
// lock file. tmuxtest.Isolate proves the isolation before any teardown runs.
func newIsolated(t *testing.T, sessions ...string) *Daemon {
	t.Helper()

	root := tmuxtest.Isolate(t)

	// The socket path is still derived from XDG_DATA_HOME in three separate
	// places, so both of these are needed to keep the daemon's store, socket and
	// lock file inside the scratch dir.
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", state, err)
	}
	t.Setenv("XDG_DATA_HOME", state)
	t.Setenv("GOTOMUX_DATA_DIR", state)
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
	t.Setenv("GOTOMUX_DATA_DIR", state)
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
	t.Setenv("GOTOMUX_DATA_DIR", state)
	t.Setenv("XDG_RUNTIME_DIR", state)
	t.Setenv("GOTOMUX_POLL_INTERVAL", "1s")

	if err := exec.Command("tmux", "kill-server").Run(); err != nil {
		t.Fatalf("kill-server: %v", err)
	}
	d, err := New(config.Load())
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
