package daemon

import (
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/gitinfo"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/zoxide"
)

// gitConcurrency bounds the .git/HEAD reads during a sync. They are off the IPC
// response path entirely, so this only needs to stay polite to the disk.
const gitConcurrency = 8

// servedPaths lists every path that appears in the payload, so git labels cover
// presets and zoxide rows too. Only live-session paths used to be resolved, which
// meant preset and zoxide rows never showed a branch on the daemon path no matter
// how long it ran.
func servedPaths(sessions []tmux.LiveSession, presets []store.PresetMeta, zox []store.ZoxRow) []string {
	out := make([]string, 0, len(sessions)+len(presets)+len(zox))
	for _, s := range sessions {
		out = append(out, s.Path)
	}
	for _, p := range presets {
		out = append(out, p.Cwd)
	}
	for _, z := range zox {
		out = append(out, z.Path)
	}
	return out
}

type Daemon struct {
	cc           *tmux.ControlConn
	ctl          *tmux.Ctl
	st           *store.Store
	stPath       string
	stMu         sync.Mutex
	cfg          *config.Config
	lastSeen     map[string]int64
	lastSeenMu   sync.Mutex
	stateVersion atomic.Int64

	cachedSessions []tmux.LiveSession
	cachedPresets  []store.PresetMeta
	cachedZoxide   []store.ZoxRow
	// cachedPairs is keyed by session name. Pair scores are relative to a current
	// session, and the *client's* session is what matters — the daemon used to
	// compute a single map from its own context, which has no $TMUX and so always
	// resolved to "", silently disabling co-occurrence for every served request.
	// Precomputing per live session keeps SQLite off the response path.
	cachedPairs       map[string]map[string]int64
	cachedUsage       map[string]store.Usage
	cachedGitBranches map[string]string
	cachedSticky      string
	cacheMu           sync.RWMutex

	stopCh   chan struct{}
	stopOne  sync.Once
	sockPath string
	wg       sync.WaitGroup

	// ln is the IPC listener, held so Shutdown can unblock ServeIPC's Accept.
	lnMu sync.Mutex
	ln   net.Listener

	// ready reports that the most recent sync saw both tmux and the DB, so the
	// served payload is complete. syncedAt is the unix time of that sync.
	// Clients use these to distinguish "daemon has real data" from "daemon
	// answered" — an OK response with an empty payload is otherwise
	// indistinguishable from success.
	ready    atomic.Bool
	syncedAt atomic.Int64

	// error governance
	storeErrs atomic.Int64
	ccErrs    atomic.Int64
	ccOK      atomic.Bool // last control-mode exchange succeeded
	startedAt time.Time
}

// ccTimeouts reads the transport's reply-timeout counter. It lives on the
// connection, not here — the daemon previously declared the field, shipped it on
// the wire, and never incremented it.
func (d *Daemon) ccTimeouts() int64 {
	if d.cc == nil {
		return 0
	}
	return d.cc.Timeouts()
}

func New(cfg *config.Config) (*Daemon, error) {
	ctl, err := tmux.New()
	if err != nil {
		return nil, err
	}
	stDir := cfg.ResolveDataDir()
	if err := os.MkdirAll(stDir, 0o755); err != nil {
		return nil, err
	}
	stPath := filepath.Join(stDir, "state.db")
	st, err := store.OpenWithConfig(cfg)
	if err != nil {
		return nil, err
	}

	d := &Daemon{
		// Not attached yet: the daemon must not create the tmux server. See
		// ensureControl.
		cc: tmux.NewControl(), ctl: ctl, st: st, stPath: stPath, cfg: cfg,
		lastSeen: map[string]int64{}, sockPath: cfg.SocketPath(),
		stopCh: make(chan struct{}), startedAt: time.Now(),
	}
	d.ensureControl()
	d.syncZoxide()
	d.syncNow()
	d.wg.Add(2)
	go d.pollLoop()
	go d.watchEvents()
	// Off the startup path: this is bulk I/O whose only purpose is to be finished
	// before the user's first picker open, not before the socket is bound.
	go prewarm(cfg)
	return d, nil
}

// Shutdown unblocks ServeIPC by closing the listener, so a signal handler can
// end the accept loop without racing on Close.
func (d *Daemon) Shutdown() {
	d.lnMu.Lock()
	ln := d.ln
	d.lnMu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// Close is idempotent: main may reach it down either the signal path or the
// accept-error path.
func (d *Daemon) Close() {
	d.stopOne.Do(func() {
		close(d.stopCh)
		d.wg.Wait()
		d.stMu.Lock()
		if d.st != nil {
			d.st.Close()
			d.st = nil
		}
		d.stMu.Unlock()
		if d.cc != nil {
			d.cc.Close()
		}
	})
}

func (d *Daemon) setListener(l net.Listener) {
	d.lnMu.Lock()
	d.ln = l
	d.lnMu.Unlock()
}

// ensureControl attaches the control client, but only to a tmux server that
// already exists.
//
// It deliberately never runs `tmux start-server`. tmux registers a systemd
// transient scope per pane, parented under the unit that started the server — so a
// server started from gotomuxd.service made every pane of every session a child
// of that service, and `systemctl stop gotomuxd` tore all of them down. Measured
// on a real machine: the journal logged "Stopping tmux child pane N" for every
// pane and left the server with zero sessions. KillMode=process saves the server
// process but not the pane scopes, so it is necessary and not sufficient.
//
// The daemon observes tmux; it does not create it. When no server is running there
// is nothing to observe, so the daemon stays not-ready and clients fall back to
// standalone until a server appears.
//
// `exit-empty off` is no longer set either: it was a global mutation of the user's
// server, and it is redundant while the daemon owns a hidden session — the server
// never reaches zero sessions.
func (d *Daemon) ensureControl() {
	if d.cc.Alive() {
		return
	}
	if !tmux.ServerRunning() {
		return
	}
	if err := d.cc.Reconnect(); err != nil {
		log.Printf("[cc] [WARN] attach control client: %v", err)
		return
	}
	log.Printf("[cc] [INFO] control client attached")
}

func (d *Daemon) ensureDB() {
	d.stMu.Lock()
	defer d.stMu.Unlock()
	if d.st == nil {
		return
	}
	if err := d.st.Ping(); err != nil {
		d.storeErrs.Add(1)
		log.Printf("[store] [ERROR] ping: %v — reopening", err)
		d.st.Close()
		if st, err := store.OpenWithConfig(d.cfg); err == nil {
			d.st = st
			log.Printf("[store] [INFO] reopened")
		} else {
			log.Printf("[store] [ERROR] reopen: %v", err)
			d.st = nil
		}
	}
}

// ensureSocket exits the daemon if its socket file has disappeared.
//
// Once the inode is unlinked, no client can ever reach this process again — but
// the accept loop happily keeps running on the orphaned inode, and the flock is
// still held, so a replacement daemon cannot start either. That is a permanently
// wedged state. Shutting down releases the lock; the next CLI invocation dials,
// fails, and autostarts a working daemon.
//
// This used to only log the problem.
func (d *Daemon) ensureSocket() {
	// Only meaningful while actually serving; a daemon under test has no listener.
	d.lnMu.Lock()
	serving := d.ln != nil
	d.lnMu.Unlock()
	if !serving {
		return
	}
	if _, err := os.Stat(d.sockPath); err == nil {
		return
	}
	log.Printf("[ipc] [ERROR] socket %s is gone — exiting so a replacement can bind", d.sockPath)
	d.Shutdown()
}

// Two separate commands, not one line joined by ";": control mode quotes a ";"
// argument into a literal, which makes list-sessions fail outright. SendLines
// concatenates the blocks and ParseLiveOutput keys off the S/P tags, so the
// combined parse is unaffected.
var (
	listSessCmd  = []string{"list-sessions", "-F", tmux.ListSessFmt}
	listPanesCmd = []string{"list-panes", "-s", "-F", tmux.ListPanesFmt}
)

// listLiveViaControl returns live sessions, or nil when tmux could not be read.
//
// nil means "unknown", not "none" — callers use it to decide whether the payload
// is complete, so an empty-but-non-nil slice must be returned when tmux answers
// with no user sessions.
func (d *Daemon) listLiveViaControl() []tmux.LiveSession {
	if !d.cc.Alive() {
		d.ensureControl()
		if !d.cc.Alive() {
			d.ccOK.Store(false)
			return nil
		}
	}
	raw, err := d.cc.SendLines(context.Background(), listSessCmd, listPanesCmd)
	if err != nil {
		d.ccErrs.Add(1)
		d.ccOK.Store(false)
		log.Printf("[cc] [ERROR] send: %v — reconnecting", err)
		if rerr := d.cc.Reconnect(); rerr != nil {
			log.Printf("[cc] [ERROR] reconnect: %v", rerr)
			return nil
		}
		log.Printf("[cc] [INFO] reconnected")
		raw, err = d.cc.SendLines(context.Background(), listSessCmd, listPanesCmd)
		if err != nil {
			d.ccErrs.Add(1)
			log.Printf("[cc] [ERROR] after reconnect: %v", err)
			return nil
		}
	}
	d.ccOK.Store(true)
	return withoutHidden(tmux.ParseLiveOutput(raw))
}

// withoutHidden drops the daemon's own control session, which is a real session
// as far as tmux is concerned and would otherwise appear in the picker.
// Always returns non-nil so "only the hidden session exists" stays distinct from
// "tmux unreadable".
func withoutHidden(in []tmux.LiveSession) []tmux.LiveSession {
	out := make([]tmux.LiveSession, 0, len(in))
	for _, s := range in {
		if tmux.IsHiddenSession(s.Name) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// syncNow refreshes every cache the IPC layer serves.
//
// The tmux half and the SQLite half are independent: presets, usage and pairs
// have no tmux dependency, so a broken control connection must not skip them.
// Previously one nil from tmux returned early and left every SQLite-backed
// cache permanently empty.
func (d *Daemon) syncNow() {
	d.ensureDB()

	sessions := d.listLiveViaControl()
	tmuxOK := sessions != nil

	if tmuxOK {
		d.diffTelemetry(sessions)
	}

	// Query outside cacheMu. Holding the writer lock across SQLite blocks every
	// concurrent list request, on a pool with MaxOpenConns(1) — the opposite of
	// keeping work off the IPC response path.
	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()

	var (
		pairs   map[string]map[string]int64
		usage   map[string]store.Usage
		presets []store.PresetMeta
		sticky  string
	)
	if st != nil {
		now := time.Now().Unix()
		// One map per live session, so a client can look up the scores for its own
		// session without the daemon running a query on the response path.
		pairs = make(map[string]map[string]int64, len(sessions))
		for _, s := range sessions {
			if p, err := st.PairScores(s.Name, now); err == nil && len(p) > 0 {
				pairs[s.Name] = p
			}
		}
		usage, _ = st.AllUsage()
		if pm, err := st.ListMeta(); err == nil {
			presets = pm
		}
		sticky = template.StickyLabel(st)
	}

	// Git labels are recomputed every sync, not once. They used to be resolved
	// lazily on the first list request and then cached for the daemon's entire
	// life, so a `git checkout` was never reflected — and the client's PreloadCache
	// wrote those stale values into a map that wins over fresh local reads.
	d.cacheMu.RLock()
	zox := d.cachedZoxide
	d.cacheMu.RUnlock()
	branches := gitinfo.Labels(servedPaths(sessions, presets, zox), gitConcurrency)

	d.cacheMu.Lock()
	if tmuxOK {
		d.cachedSessions = sessions
	}
	if st != nil {
		d.cachedPairs, d.cachedUsage, d.cachedPresets = pairs, usage, presets
		d.cachedSticky = sticky
	}
	d.cachedGitBranches = branches
	d.cacheMu.Unlock()

	// Any publish advances the generation counter — it is a debugging signal,
	// not a cache-validity token, so it must not pretend that only session-set
	// changes matter.
	d.stateVersion.Add(1)

	ready := tmuxOK && st != nil
	d.ready.Store(ready)
	if ready {
		d.syncedAt.Store(time.Now().Unix())
	}
}

// diffTelemetry records opens/pairs for sessions that are new or newly
// re-attached since the last sync, and forgets sessions that vanished.
func (d *Daemon) diffTelemetry(sessions []tmux.LiveSession) {
	d.lastSeenMu.Lock()
	defer d.lastSeenMu.Unlock()
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
	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()
	if st == nil {
		return
	}
	log.Printf("[store] [INFO] telemetry: %s", name)
	st.RecordOpen(name)
	others := make([]string, 0, len(all))
	for _, s := range all {
		if s.Name != name {
			others = append(others, s.Name)
		}
	}
	if len(others) > 0 {
		st.RecordPairsWithLive(name, others)
	}
}

// syncZoxide refreshes the zoxide row cache.
//
// Rows come from internal/zoxide, the same derivation the picker uses. This
// package used to build them itself with filepath.Base for the name, the raw
// path, and time.Now() as recency — three divergences from the picker, the last
// of which put every directory ahead of every live session in the ranking.
func (d *Daemon) syncZoxide() {
	paths := zoxide.Query()
	if len(paths) == 0 {
		return
	}
	sig := zoxide.Signature(paths)

	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()

	// Deriving rows resolves a project root per path — ~0.9ms each, ~270ms for a
	// 300-entry list. Skip it entirely when the path list has not changed, which is
	// the common case for a 60s refresh cycle.
	if st != nil {
		if cached, _, storedSig, ok := st.LoadZox(); ok && storedSig == sig {
			d.cacheMu.Lock()
			same := len(d.cachedZoxide) == len(cached)
			if !same {
				d.cachedZoxide = cached
			}
			d.cacheMu.Unlock()
			if !same {
				d.stateVersion.Add(1)
			}
			return
		}
	}

	rows := zoxide.Rows(paths)
	if len(rows) == 0 {
		return
	}
	if st != nil {
		_ = st.SaveZox(rows, sig)
	}

	d.cacheMu.Lock()
	d.cachedZoxide = rows
	d.cacheMu.Unlock()
	d.stateVersion.Add(1)
}

// zoxideRefresh bounds how stale the zoxide row cache may get. It used to be
// synced twice at startup and then never again for the daemon's whole life.
const zoxideRefresh = 60 * time.Second

// pruneEvery is how often learned-row housekeeping runs. It used to run inside
// store.OpenWithConfig, i.e. on every CLI cold start; the daemon is the right
// place for work whose only requirement is "eventually".
const pruneEvery = time.Hour

func (d *Daemon) prune() {
	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()
	if st != nil {
		st.Prune()
	}
}

// checkpoint folds the WAL back into the DB. Runs on the zoxide cadence, not the
// prune cadence: a PASSIVE checkpoint on a small WAL is sub-millisecond, and doing
// it often is what keeps the WAL small — letting it grow for an hour is what makes
// the eventual checkpoint expensive and every client read slower in the meantime.
func (d *Daemon) checkpoint() {
	d.stMu.Lock()
	st := d.st
	d.stMu.Unlock()
	if st != nil {
		st.Checkpoint()
	}
}

func (d *Daemon) pollLoop() {
	defer d.wg.Done()
	interval := 10 * time.Second
	if d.cfg != nil && d.cfg.PollInterval > 0 {
		interval = d.cfg.PollInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lastZox := time.Now()
	lastPrune := time.Now()

	for {
		select {
		case <-d.stopCh:
			return
		case <-tick.C:
			// Re-attach if a server has appeared (or ours went away). Never starts
			// a server.
			d.ensureControl()
			d.ensureSocket()
			d.syncNow() // ensureDB runs inside syncNow
			if time.Since(lastZox) >= zoxideRefresh {
				lastZox = time.Now()
				d.syncZoxide()
				d.checkpoint()
			}
			if time.Since(lastPrune) >= pruneEvery {
				lastPrune = time.Now()
				d.prune()
			}
		}
	}
}

// watchEvents resyncs on control-mode notifications so session create/kill shows
// up immediately instead of at the next tick.
//
// This does NOT make the poll redundant. Events cover membership and names only:
// creating a session emits %sessions-changed (there is no %session-created),
// killing emits it too, renaming emits %session-renamed. Nothing is emitted when
// session_activity advances — and that advances on any pane output and feeds
// LiveSession recency. Events give membership freshness, the poll gives timestamp
// freshness; both are load-bearing.
func (d *Daemon) watchEvents() {
	defer d.wg.Done()
	const debounce = 50 * time.Millisecond
	events := d.cc.Events()
	for {
		select {
		case <-d.stopCh:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !membershipEvent(ev) {
				continue
			}
			// One change produces a burst (%window-add + %sessions-changed +
			// %session-changed); coalesce until the stream goes quiet.
		coalesce:
			for {
				select {
				case <-d.stopCh:
					return
				case <-events:
				case <-time.After(debounce):
					break coalesce
				}
			}
			d.syncNow()
		}
	}
}

// membershipEvent reports whether a notification can change the session set,
// their names, or their attach state (which feeds recency).
func membershipEvent(line string) bool {
	name := line
	if i := strings.IndexByte(line, ' '); i >= 0 {
		name = line[:i]
	}
	switch name {
	case "%sessions-changed", "%session-changed", "%session-renamed",
		"%session-window-changed", "%window-add", "%window-close",
		"%unlinked-window-add", "%unlinked-window-close",
		"%client-session-changed", "%client-detached":
		return true
	}
	return false
}
