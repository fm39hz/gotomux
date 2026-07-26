package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type Request struct {
	Cmd  string `json:"cmd"`
	Name string `json:"name,omitempty"`
	// SessID is the caller's own tmux session id ("$0"), read from its $TMUX with
	// no fork. The daemon cannot infer it: the daemon process has no $TMUX, so the
	// CtxSess/CtxPath fields it used to send always described nothing.
	SessID string `json:"sess_id,omitempty"`
}

type Response struct {
	OK bool `json:"ok"`
	// Ready reports that the last sync saw BOTH tmux and the DB, i.e. the
	// payload is complete rather than merely well-formed. Clients must treat
	// !Ready as "fall back to standalone" — OK alone says nothing about
	// completeness, which is exactly how an empty payload used to pass as a
	// successful response.
	Ready bool `json:"ready,omitempty"`
	// SyncedAt is the unix time of the last Ready sync. Clients reject payloads
	// older than a few poll intervals so a wedged daemon cannot serve stale data.
	SyncedAt int64 `json:"synced_at,omitempty"`
	// Version is a monotonic state generation, for debugging only. There is no
	// conditional-request protocol: the CLI is a short-lived process with no
	// cross-run cache, so a client-side version is meaningless.
	Version  int64              `json:"version,omitempty"`
	Error    string             `json:"error,omitempty"`
	Sessions []tmux.LiveSession `json:"sessions,omitempty"`
	Presets  []store.PresetMeta `json:"presets,omitempty"`
	// Pairs holds co-occurrence scores for the session named by Request.SessID.
	Pairs map[string]int64 `json:"pairs,omitempty"`
	// StickyLabel is the sticky shape's display label, served so the client needs
	// no store open just to render the header.
	StickyLabel string                 `json:"sticky_label,omitempty"`
	Usage       map[string]store.Usage `json:"usage,omitempty"`
	GitBranches map[string]string      `json:"git_branches,omitempty"`
	Zoxide      []store.ZoxRow         `json:"zoxide,omitempty"`

	// status response fields
	StatusCC    bool  `json:"status_cc,omitempty"`
	StatusStore bool  `json:"status_store,omitempty"`
	CCErrs      int64 `json:"cc_errs,omitempty"`
	CCTimeouts  int64 `json:"cc_timeouts,omitempty"`
	StoreErrs   int64 `json:"store_errs,omitempty"`
	Uptime      int64 `json:"uptime,omitempty"`
}

// listenWithGuard binds the Unix socket with stale-socket detection.
// Tries to dial first; if a live daemon responds, returns an error.
// If the socket is stale (dial fails), removes and re-listens.
func listenWithGuard(sock string) (net.Listener, error) {
	l, err := net.Listen("unix", sock)
	if err == nil {
		return l, nil
	}
	// Bind failed — check if a live daemon is already listening
	if conn, dialErr := net.DialTimeout("unix", sock, 100*time.Millisecond); dialErr == nil {
		conn.Close()
		return nil, fmt.Errorf("daemon already running on %s", sock)
	}
	// Stale socket — clean and retry
	os.Remove(sock)
	l, err = net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sock, err)
	}
	return l, nil
}

// acquireLock uses flock to prevent multiple daemon instances.
// Lock auto-releases when the process exits (no stale cleanup needed).
func acquireLock(sockPath string) (func(), error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	_ = os.MkdirAll(runtimeDir, 0o750)

	h := fnv.New64a()
	_, _ = h.Write([]byte("gotomux-" + sockPath))
	lockPath := filepath.Join(runtimeDir, fmt.Sprintf("gotomux-%x.lock", h.Sum64()))

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("daemon already running (lock %s)", lockPath)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(lockPath)
	}, nil
}

// ServeIPC listens on the Unix socket and handles IPC requests.
//
// The path comes from the Daemon, which got it from Config — so the socket
// ensureSocket watches and the socket bound here are the same value by
// construction, and GOTOMUX_DATA_DIR relocates both.
func ServeIPC(d *Daemon) error {
	sock := d.sockPath
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}

	unlock, err := acquireLock(sock)
	if err != nil {
		return err
	}
	defer unlock()

	l, err := listenWithGuard(sock)
	if err != nil {
		return err
	}
	defer os.Remove(sock)
	d.setListener(l)

	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go d.handleConn(conn)
	}
}

// connIdle bounds how long a conn may sit between requests. Connections are
// now multi-request, so without this a client that never closes would pin a
// goroutine for the daemon's lifetime.
const connIdle = 30 * time.Second

// handleConn serves requests until the peer closes. It must stay a loop: the
// CLI sends "list" and then "connect" (or "freeze") on the same connection, and
// a one-shot handler silently dropped that second request — which is what killed
// all open/pair telemetry and broke `gotomux -f`.
func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(connIdle))
		var req Request
		if err := dec.Decode(&req); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrDeadlineExceeded) {
				log.Printf("[ipc] [WARN] decode request: %v", err)
				_ = enc.Encode(Response{OK: false, Error: "bad request"})
			}
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		if err := d.serveRequest(enc, req); err != nil {
			return
		}
	}
}

// serveRequest writes exactly one Response per request. Every branch responds,
// including unknown commands — a silently closed connection just makes the
// client wait out its full decode timeout.
func (d *Daemon) serveRequest(enc *json.Encoder, req Request) error {
	switch req.Cmd {
	case "ping":
		return enc.Encode(Response{OK: true})
	case "status":
		return enc.Encode(d.buildStatusResponse())
	case "list":
		return enc.Encode(d.buildListResponse(req.SessID))
	case "connect":
		d.handleConnect(req.Name)
		return enc.Encode(Response{OK: true})
	case "freeze":
		if err := d.handleFreeze(req.Name); err != nil {
			return enc.Encode(Response{OK: false, Error: err.Error()})
		}
		return enc.Encode(Response{OK: true})
	default:
		return enc.Encode(Response{OK: false, Error: "unknown cmd " + req.Cmd})
	}
}

func (d *Daemon) buildStatusResponse() Response {
	stOK := false
	d.stMu.Lock()
	if d.st != nil {
		stOK = d.st.Ping() == nil
	}
	d.stMu.Unlock()

	return Response{
		OK:    true,
		Ready: d.ready.Load(), SyncedAt: d.syncedAt.Load(),
		// StatusCC tracks whether the last control-mode exchange actually
		// succeeded. It used to be `d.cc != nil`, which is never false — the
		// daemon reported healthy while logging an error every single poll.
		StatusCC: d.ccOK.Load(), StatusStore: stOK,
		CCErrs: d.ccErrs.Load(), CCTimeouts: d.ccTimeouts(),
		StoreErrs: d.storeErrs.Load(),
		Uptime:    int64(time.Since(d.startedAt).Seconds()),
	}
}

func (d *Daemon) handleConnect(name string) {
	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()
	if name == "" || st == nil {
		return
	}
	log.Printf("[store] [INFO] connect: %s", name)
	st.RecordOpen(name)
	_ = st.Touch(name) // no-op unless name is a saved preset
	sessions := d.listLiveViaControl()
	if sessions != nil {
		others := make([]string, 0, len(sessions))
		for _, s := range sessions {
			if s.Name != name {
				others = append(others, s.Name)
			}
		}
		if len(others) > 0 {
			st.RecordPairsWithLive(name, others)
		}
	}
	d.lastSeenMu.Lock()
	d.lastSeen[name] = time.Now().Unix()
	d.lastSeenMu.Unlock()
	d.stateVersion.Add(1)
}

func (d *Daemon) handleFreeze(name string) error {
	if name == "" {
		return fmt.Errorf("freeze: empty name")
	}
	if d.ctl == nil {
		return fmt.Errorf("freeze: tmux unavailable")
	}
	// Copy the store pointer under stMu like every other reader. Reading d.st
	// directly raced with ensureDB, which Closes and replaces that same pointer
	// on a failed Ping — an unsynchronized read plus a use-after-Close, during a
	// freeze that can take seconds.
	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()
	if st == nil {
		return fmt.Errorf("freeze: store unavailable")
	}
	log.Printf("freeze: %s", name)
	_, _, err := template.FreezeRemember(d.ctl, st, name)
	return err
}

// buildListResponse assembles the payload. sessID is the client's tmux session id
// ("$0" from its own $TMUX), used to pick the right pair-score map.
func (d *Daemon) buildListResponse(sessID string) Response {
	d.cacheMu.RLock()
	sessions := d.cachedSessions
	presets := d.cachedPresets
	zoxide := d.cachedZoxide
	allPairs := d.cachedPairs
	usage := d.cachedUsage
	gitBranches := d.cachedGitBranches
	sticky := d.cachedSticky
	d.cacheMu.RUnlock()

	// Look up, never compute: the maps are built per live session during sync.
	var pairs map[string]int64
	if cur, ok := tmux.FindByID(sessions, sessID); ok {
		pairs = allPairs[cur.Name]
	}

	return Response{OK: true,
		Ready: d.ready.Load(), SyncedAt: d.syncedAt.Load(),
		Version:  d.stateVersion.Load(),
		Sessions: sessions, Presets: presets,
		Pairs: pairs, Usage: usage, StickyLabel: sticky,
		GitBranches: gitBranches, Zoxide: zoxide}
}
