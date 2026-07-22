package daemon

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Request/Response for IPC — JSON over Unix socket.
type Request struct {
	Cmd string `json:"cmd"`
}

type Response struct {
	OK      bool                `json:"ok"`
	Error   string              `json:"error,omitempty"`
	Sessions []tmux.LiveSession `json:"sessions,omitempty"`
	Presets []store.PresetMeta  `json:"presets,omitempty"`
	CtxSess string              `json:"ctx_sess,omitempty"`
	CtxPath string              `json:"ctx_path,omitempty"`
	Pairs   map[string]int64    `json:"pairs,omitempty"`
	Usage   map[string]store.Usage `json:"usage,omitempty"`
}

// ServeIPC listens on Unix socket and handles "list" requests.
func ServeIPC(d *Daemon) error {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	sock := filepath.Join(dir, "gotomux", "gotomux.sock")
	os.Remove(sock)

	l, err := net.Listen("unix", sock)
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
	if req.Cmd == "list" {
		d.handleList(conn)
	}
	// only "list" command supported
}

func (d *Daemon) handleList(conn net.Conn) {
	resp := d.buildListResponse()
	json.NewEncoder(conn).Encode(resp)
}

func (d *Daemon) buildListResponse() Response {
	// Live sessions via tmux -C (0 fork)
	var sessions []tmux.LiveSession
	if raw, err := d.cc.Send("list-sessions", "-F", "S\t#{session_name}\t#{session_windows}\t#{session_path}\t#{session_last_attached}\t#{session_activity}\t#{session_created}\t#{session_attached}", ";", "list-panes", "-s", "-F", "P\t#{session_name}\t#{pane_current_command}\t#{?pane_active,1,0}\t#{?pane_dead,1,0}"); err == nil {
		sessions = tmux.ParseLiveOutput(raw)
	}

	// Presets + context via SQLite + exec.Command
	var presets []store.PresetMeta
	if d.st != nil {
		presets, _ = d.st.ListMeta()
	}

	ctxSess := d.ctl.CurrentSession()
	ctxPath := d.ctl.CurrentSessionPath()

	var pairs map[string]int64
	var usage map[string]store.Usage
	if ctxSess != "" && d.st != nil {
		pairs, _ = d.st.PairScores(ctxSess, time.Now().Unix())
	}
	if d.st != nil {
		usage, _ = d.st.AllUsage()
	}

	return Response{OK: true, Sessions: sessions, Presets: presets,
		CtxSess: ctxSess, CtxPath: ctxPath, Pairs: pairs, Usage: usage}
}
