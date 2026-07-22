package daemon

import (
	"log"
	"os/exec"
	"time"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type Daemon struct {
	cc       *tmux.ControlConn
	ctl      *tmux.Ctl
	st       *store.Store
	lastSeen map[string]int64
}

func New() (*Daemon, error) {
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
	st, err := store.Open()
	if err != nil {
		cc.Close()
		return nil, err
	}
	d := &Daemon{cc: cc, ctl: ctl, st: st, lastSeen: map[string]int64{}}
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
	raw, err := d.cc.Send("list-sessions", "-F", "S\t#{session_name}\t#{session_windows}\t#{session_path}\t#{session_last_attached}\t#{session_activity}\t#{session_created}\t#{session_attached}", ";", "list-panes", "-s", "-F", "P\t#{session_name}\t#{pane_current_command}\t#{?pane_active,1,0}\t#{?pane_dead,1,0}")
	if err != nil {
		return nil
	}
	return tmux.ParseLiveOutput(raw)
}

func (d *Daemon) syncNow() {
	sessions := d.listLiveViaControl()
	if sessions == nil {
		return
	}
	for _, s := range sessions {
		if prev, ok := d.lastSeen[s.Name]; !ok || s.LastAttached > prev {
			d.recordTelemetry(s.Name, sessions)
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
		}
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

func (d *Daemon) pollLoop() {
	for {
		time.Sleep(2 * time.Second)
		d.syncNow()
	}
}
