package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
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

// ServeIPC listens on Unix socket and handles IPC requests.
func ServeIPC(d *Daemon) error {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	sock := filepath.Join(dir, "gotomux", "gotomux.sock")

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
		return
	}
	switch req.Cmd {
	case "ping":
		json.NewEncoder(conn).Encode(Response{OK: true})
	case "list":
		v := d.stateVersion.Load()
		if req.Version == v {
			json.NewEncoder(conn).Encode(Response{OK: true, Version: v})
			return
		}
		resp := d.buildListResponse()
		resp.Version = v
		json.NewEncoder(conn).Encode(resp)
	case "connect":
		d.handleConnect(req.Name)
		json.NewEncoder(conn).Encode(Response{OK: true})
	case "freeze":
		err := d.handleFreeze(req.Name)
		if err != nil {
			json.NewEncoder(conn).Encode(Response{OK: false, Error: err.Error()})
			return
		}
		json.NewEncoder(conn).Encode(Response{OK: true})
	}
}

func (d *Daemon) handleConnect(name string) {
	if name == "" || d.st == nil {
		return
	}
	log.Printf("connect: %s", name)
	d.st.RecordOpen(name)
	sessions := d.listLiveViaControl()
	if sessions != nil {
		others := make([]string, 0, len(sessions))
		for _, s := range sessions {
			if s.Name != name {
				others = append(others, s.Name)
			}
		}
		if len(others) > 0 {
			d.st.RecordPairsWithLive(name, others)
		}
	}
	d.lastSeen[name] = time.Now().Unix()
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
	var sessions []tmux.LiveSession
	if raw, err := d.cc.Send(context.Background(), "list-sessions", "-F", "S\t#{session_name}\t#{session_windows}\t#{session_path}\t#{session_last_attached}\t#{session_activity}\t#{session_created}\t#{session_attached}", ";", "list-panes", "-s", "-F", "P\t#{session_name}\t#{pane_current_command}\t#{?pane_active,1,0}\t#{?pane_dead,1,0}"); err == nil {
		sessions = tmux.ParseLiveOutput(raw)
	}

	var presets []store.PresetMeta
	if d.st != nil {
		presets, _ = d.st.ListMeta()
	}

	// Use cached values from syncNow — avoids SQLite queries per request.
	ctxSess := d.ctl.CurrentSession(context.Background())
	ctxPath := d.ctl.CurrentSessionPath(context.Background())

	d.cacheMu.RLock()
	pairs := d.cachedPairs
	usage := d.cachedUsage
	d.cacheMu.RUnlock()

	return Response{OK: true, Sessions: sessions, Presets: presets,
		CtxSess: ctxSess, CtxPath: ctxPath, Pairs: pairs, Usage: usage}
}
