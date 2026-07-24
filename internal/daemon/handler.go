package daemon

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
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
	Cmd     string `json:"cmd"`
	Name    string `json:"name,omitempty"`
	Version int64  `json:"version,omitempty"`
}

type Response struct {
	OK      bool                `json:"ok"`
	Version int64               `json:"version,omitempty"`
	Error   string              `json:"error,omitempty"`
	Sessions []tmux.LiveSession `json:"sessions,omitempty"`
	Presets []store.PresetMeta  `json:"presets,omitempty"`
	CtxSess string              `json:"ctx_sess,omitempty"`
	CtxPath string              `json:"ctx_path,omitempty"`
	Pairs   map[string]int64    `json:"pairs,omitempty"`
	Usage   map[string]store.Usage `json:"usage,omitempty"`

	// status response fields
	StatusCC     bool  `json:"status_cc,omitempty"`
	StatusStore  bool  `json:"status_store,omitempty"`
	CCErrs       int64 `json:"cc_errs,omitempty"`
	CCTimeouts   int64 `json:"cc_timeouts,omitempty"`
	StoreErrs    int64 `json:"store_errs,omitempty"`
	Uptime       int64 `json:"uptime,omitempty"`
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

// ServeIPC listens on Unix socket and handles IPC requests.
func ServeIPC(d *Daemon) error {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	sock := filepath.Join(dir, "gotomux", "gotomux.sock")

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

	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go d.handleConn(conn)
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		log.Printf("[ipc] [WARN] decode request: %v", err)
		json.NewEncoder(conn).Encode(Response{OK: false, Error: "bad request"})
		return
	}
	enc := json.NewEncoder(conn)
	switch req.Cmd {
	case "ping":
		enc.Encode(Response{OK: true})
	case "status":
		enc.Encode(d.buildStatusResponse())
	case "list":
		v := d.stateVersion.Load()
		if req.Version == v {
			enc.Encode(Response{OK: true, Version: v})
			return
		}
		resp := d.buildListResponse()
		resp.Version = v
		enc.Encode(resp)
	case "connect":
		d.handleConnect(req.Name)
		enc.Encode(Response{OK: true})
	case "freeze":
		if err := d.handleFreeze(req.Name); err != nil {
			enc.Encode(Response{OK: false, Error: err.Error()})
			return
		}
		enc.Encode(Response{OK: true})
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
		OK: true,
		StatusCC: d.cc != nil, StatusStore: stOK,
		CCErrs: d.ccErrs.Load(), StoreErrs: d.storeErrs.Load(),
		Uptime: int64(time.Since(d.startedAt).Seconds()),
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
	if d.st == nil {
		return fmt.Errorf("freeze: store unavailable")
	}
	log.Printf("freeze: %s", name)
	_, _, err := template.FreezeRemember(d.ctl, d.st, name)
	return err
}

func (d *Daemon) buildListResponse() Response {
	d.cacheMu.RLock()
	sessions := d.cachedSessions
	presets := d.cachedPresets
	ctxSess, ctxPath := d.ctxSess, d.ctxPath
	pairs := d.cachedPairs
	usage := d.cachedUsage
	d.cacheMu.RUnlock()

	return Response{OK: true, Sessions: sessions, Presets: presets,
		CtxSess: ctxSess, CtxPath: ctxPath, Pairs: pairs, Usage: usage}
}
