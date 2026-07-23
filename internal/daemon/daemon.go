package daemon

import (
	"context"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type Daemon struct {
	cc           *tmux.ControlConn
	ctl          *tmux.Ctl
	st           *store.Store
	cfg          *config.Config
	lastSeen     map[string]int64
	stateVersion atomic.Int64

	cachedPairs map[string]int64
	cachedUsage map[string]store.Usage
	cacheMu     sync.RWMutex
}

func New(cfg *config.Config) (*Daemon, error) {
	exec.Command("tmux", "start-server").Run()
	exec.Command("tmux", "set-option", "-g", "exit-empty", "off").Run()

	cc, err := tmux.StartControl()
	if err != nil {
		return nil, err
	}
	ctl, err := tmux.New()
	if err != nil {
		cc.Close()
		return nil, err
	}
	st, err := store.OpenWithConfig(cfg)
	if err != nil {
		cc.Close()
		return nil, err
	}
	d := &Daemon{cc: cc, ctl: ctl, st: st, cfg: cfg, lastSeen: map[string]int64{}}
	d.syncZoxide()
	d.syncNow()
	go d.pollLoop()
	return d, nil
}

func (d *Daemon) Close() {
	if d.st != nil {
		d.st.Close()
	}
	if d.cc != nil {
		d.cc.Close()
	}
}

func (d *Daemon) listLiveViaControl() []tmux.LiveSession {
	raw, err := d.cc.Send(context.Background(), "list-sessions", "-F", "S\t#{session_name}\t#{session_windows}\t#{session_path}\t#{session_last_attached}\t#{session_activity}\t#{session_created}\t#{session_attached}", ";", "list-panes", "-s", "-F", "P\t#{session_name}\t#{pane_current_command}\t#{?pane_active,1,0}\t#{?pane_dead,1,0}")
	if err == nil {
		return tmux.ParseLiveOutput(raw)
	}
	log.Printf("listLiveViaControl: %v — attempting reconnect", err)
	if rerr := d.cc.Reconnect(); rerr != nil {
		log.Printf("reconnect failed: %v", rerr)
		return nil
	}
	log.Printf("reconnected to tmux -C")
	raw, err = d.cc.Send(context.Background(), "list-sessions", "-F", "S\t#{session_name}\t#{session_windows}\t#{session_path}\t#{session_last_attached}\t#{session_activity}\t#{session_created}\t#{session_attached}", ";", "list-panes", "-s", "-F", "P\t#{session_name}\t#{pane_current_command}\t#{?pane_active,1,0}\t#{?pane_dead,1,0}")
	if err != nil {
		log.Printf("listLiveViaControl after reconnect: %v", err)
		return nil
	}
	return tmux.ParseLiveOutput(raw)
}

func (d *Daemon) syncNow() {
	sessions := d.listLiveViaControl()
	if sessions == nil {
		return
	}
	changed := false
	for _, s := range sessions {
		if prev, ok := d.lastSeen[s.Name]; !ok || s.LastAttached > prev {
			d.recordTelemetry(s.Name, sessions)
			changed = true
		}
		d.lastSeen[s.Name] = s.LastAttached
	}
	for name := range d.lastSeen {
		keep := false
		for _, s := range sessions {
			if s.Name == name {
				keep = true
				break
			}
		}
		if !keep {
			delete(d.lastSeen, name)
			changed = true
		}
	}

	// Precompute pairs + usage for fast IPC response
	ctxSess := d.ctl.CurrentSession(context.Background())
	d.cacheMu.Lock()
	if ctxSess != "" && d.st != nil {
		d.cachedPairs, _ = d.st.PairScores(ctxSess, time.Now().Unix())
	} else {
		d.cachedPairs = nil
	}
	if d.st != nil {
		d.cachedUsage, _ = d.st.AllUsage()
	} else {
		d.cachedUsage = nil
	}
	d.cacheMu.Unlock()

	if changed {
		d.stateVersion.Add(1)
	}
}

func (d *Daemon) recordTelemetry(name string, all []tmux.LiveSession) {
	if d.st == nil {
		return
	}
	log.Printf("telemetry: %s", name)
	d.st.RecordOpen(name)
	others := make([]string, 0, len(all))
	for _, s := range all {
		if s.Name != name {
			others = append(others, s.Name)
		}
	}
	if len(others) > 0 {
		d.st.RecordPairsWithLive(name, others)
	}
}

// syncZoxide runs zoxide query -l and caches results in the zox table.
// Best-effort; no error returned (cache stays stale if zoxide is missing).
func (d *Daemon) syncZoxide() {
	if d.st == nil {
		return
	}
	out, err := exec.Command("zoxide", "query", "-l").Output()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	var rows []store.ZoxRow
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name := filepath.Base(line)
		if name == "" || name == "." || name == "/" {
			continue
		}
		rows = append(rows, store.ZoxRow{
			Name: name, Path: line,
			Title: "[Zoxide] " + name, Recency: now,
		})
	}
	if len(rows) > 0 {
		d.st.SaveZox(rows)
	}
}

func (d *Daemon) pollLoop() {
	interval := 10 * time.Second
	if d.cfg != nil {
		interval = d.cfg.PollInterval
	}
	d.syncZoxide()
	for {
		time.Sleep(interval)
		d.syncNow()
	}
}
