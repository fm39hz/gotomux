package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
	"github.com/fm39hz/gotomux/internal/toolclass"
	"github.com/junegunn/go-shellwords"
	gotmux "github.com/zigai/gotmux/tmux"
)

func IsNamedLayout(s string) bool           { return tmuxlayout.IsNamed(s) }
func IsLayoutDump(s string) bool            { return tmuxlayout.IsDump(s) }
func LayoutTopology(s string) string        { return tmuxlayout.Topology(s) }
func LayoutForStore(s string, n int) string { return tmuxlayout.ForStore(s, n) }
func LayoutForShape(s string, n int) string { return tmuxlayout.ForShape(s, n) }
func InferSplit(s string, n int) string     { return tmuxlayout.InferSplit(s, n) }

// IsNoServerError reports whether a tmux error means the server isn't running,
// so callers can tell "nothing to list" from a real failure.
//
// It must inspect ExitError.Stderr, not just err.Error(). tmux writes "no server
// running on …" to stderr, and exec.Cmd.Output() puts that in ExitError.Stderr
// while err.Error() is only "exit status 1". Checking the message alone made this
// function always return false on the ListLive path — so its `return nil, nil`
// branch was dead code, and `gotomux -f` with no server reported
// "tmux list: exit status 1" instead of "no active sessions".
func IsNoServerError(err error) bool {
	if err == nil {
		return false
	}
	if noServerText(err.Error()) {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && noServerText(string(ee.Stderr)) {
		return true
	}
	return false
}

func noServerText(s string) bool {
	msg := strings.ToLower(s)
	switch {
	case strings.Contains(msg, "no server running"):
		return true
	case strings.Contains(msg, "error connecting to") &&
		strings.Contains(msg, "no such file or directory"):
		return true
	default:
		return false
	}
}

type Ctl struct{}

func New() (*Ctl, error) {
	return &Ctl{}, nil
}

const (
	ListSessFmt  = "S\t#{session_id}\t#{session_name}\t#{session_windows}\t#{session_path}\t#{session_last_attached}\t#{session_activity}\t#{session_created}\t#{session_attached}\t#{socket_path}\t#{pid}\t#{start_time}\t#{@gotomux_session_id_v1}"
	ListPanesFmt = "P\t#{session_name}\t#{pane_current_command}\t#{?pane_active,1,0}\t#{?pane_dead,1,0}"
)

type LiveSession struct {
	// ID is tmux's own session id ("$0"). It is carried so a client can identify
	// which session it is running inside from $TMUX alone, without forking tmux:
	// $TMUX is "<socket>,<serverpid>,<session index>" and the index matches
	// #{session_id} minus the '$'.
	ID            string `json:"id,omitempty"`
	ServerKey     string `json:"server_key,omitempty"`
	LogicalID     string `json:"logical_id,omitempty"`
	SocketPath    string `json:"socket_path,omitempty"`
	ServerPID     string `json:"server_pid,omitempty"`
	ServerStarted string `json:"server_started,omitempty"`
	Name          string `json:"name"`
	Windows       int    `json:"windows,omitempty"`
	Path          string `json:"path,omitempty"`
	LastAttached  int64  `json:"last_attached,omitempty"`
	Activity      int64  `json:"activity,omitempty"`
	Created       int64  `json:"created,omitempty"`
	Attached      int    `json:"attached,omitempty"`
	ActiveCmd     string `json:"active_cmd,omitempty"`
}

// SessionIDFromEnv extracts the current session id from a $TMUX value, or "" when
// not inside tmux. Returns the id in tmux's own "$N" form.
func SessionIDFromEnv(tmuxEnv string) string {
	if tmuxEnv == "" {
		return ""
	}
	parts := strings.Split(tmuxEnv, ",")
	if len(parts) < 3 {
		return ""
	}
	idx := strings.TrimSpace(parts[len(parts)-1])
	if idx == "" {
		return ""
	}
	if _, err := strconv.Atoi(idx); err != nil {
		return ""
	}
	return "$" + idx
}

// CurrentSessionID returns this process's tmux session id without forking.
func CurrentSessionID() string { return SessionIDFromEnv(os.Getenv("TMUX")) }

// FindByID returns the session with the given tmux id.
func FindByID(sessions []LiveSession, id string) (LiveSession, bool) {
	if id == "" {
		return LiveSession{}, false
	}
	for _, s := range sessions {
		if s.ID == id {
			return s, true
		}
	}
	return LiveSession{}, false
}

// ListLive returns the user-visible sessions with their panes attached; the
// daemon's own control session is dropped here so every exec-path consumer
// (picker sources, freeze pick list) inherits the filtering.
func (c *Ctl) ListLive(ctx context.Context) ([]LiveSession, error) {
	out, err := tmuxSequenceOutput(ctx,
		[]string{"list-sessions", "-F", ListSessFmt},
		[]string{"list-panes", "-s", "-F", ListPanesFmt},
	)
	if err != nil {
		if IsNoServerError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list: %w", err)
	}
	live := DropHidden(ParseLiveOutput(string(out)))
	if len(live) == 0 {
		return live, nil
	}
	serverKey := ServerKeyFromMetadata(live[0].SocketPath, live[0].ServerPID, live[0].ServerStarted)
	if serverKey == "" {
		serverKey = tmuxServerKey(ctx, live[0].Name)
	}
	if serverKey == "" {
		return nil, fmt.Errorf("tmux list: cannot establish server generation")
	}
	for i := range live {
		live[i].ServerKey = serverKey
	}
	return live, nil
}

// WithServerGeneration attaches a verified tmux server key to control-mode
// session rows. A missing key returns nil so callers do not expose sessions
// that cannot be safely targeted later.
func WithServerGeneration(sessions []LiveSession) []LiveSession {
	if len(sessions) == 0 {
		return sessions
	}
	serverKey := ServerKeyFromMetadata(sessions[0].SocketPath, sessions[0].ServerPID, sessions[0].ServerStarted)
	if serverKey == "" {
		return nil
	}
	for i := range sessions {
		if sessions[i].SocketPath != sessions[0].SocketPath || sessions[i].ServerPID != sessions[0].ServerPID || sessions[i].ServerStarted != sessions[0].ServerStarted {
			return nil
		}
		sessions[i].ServerKey = serverKey
	}
	return sessions
}

func ParseLiveOutput(out string) []LiveSession {
	type livePane struct {
		sname  string
		cmd    string
		active bool
		dead   bool
	}

	byName := map[string]LiveSession{}
	var order []string
	orderSeen := map[string]bool{}
	panes := map[string][]livePane{}

	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(line) < 2 || line[1] != '\t' {
			continue
		}
		fields := strings.Split(line[2:], "\t")

		switch line[0] {
		case 'S':
			if len(fields) < 8 {
				continue
			}
			name := fields[1]
			nw, _ := strconv.Atoi(fields[2])
			na, _ := strconv.Atoi(fields[7])
			byName[name] = LiveSession{
				ID: fields[0], Name: name, Windows: nw, Path: fields[3],
				LastAttached: parseUnix(fields[4]), Activity: parseUnix(fields[5]),
				Created: parseUnix(fields[6]), Attached: na,
			}
			if len(fields) >= 10 {
				live := byName[name]
				live.SocketPath, live.ServerPID = fields[8], fields[9]
				if len(fields) >= 11 {
					live.ServerStarted = fields[10]
				}
				byName[name] = live
			}
			if len(fields) >= 12 {
				live := byName[name]
				live.LogicalID = fields[11]
				byName[name] = live
			}
			if !orderSeen[name] {
				orderSeen[name] = true
				order = append(order, name)
			}
		case 'P':
			if len(fields) < 4 {
				continue
			}
			panes[fields[0]] = append(panes[fields[0]], livePane{
				cmd: fields[1], active: fields[2] == "1", dead: fields[3] == "1",
			})
		}
	}

	activeCmd := map[string]string{}
	busyCmd := map[string]string{}
	for sn, list := range panes {
		for _, p := range list {
			if p.cmd == "" || p.dead {
				continue
			}
			if _, seen := activeCmd[sn]; !seen && p.active {
				activeCmd[sn] = p.cmd
			}
			if _, seen := busyCmd[sn]; !seen && !toolclass.IsShell(p.cmd) {
				busyCmd[sn] = p.cmd
			}
		}
	}

	out2 := make([]LiveSession, 0, len(order))
	for _, name := range order {
		s := byName[name]
		cmd := activeCmd[name]
		if cmd == "" || toolclass.IsShell(cmd) {
			if b, ok := busyCmd[name]; ok {
				cmd = b
			}
		}
		s.ActiveCmd = cmd
		out2 = append(out2, s)
	}
	return out2
}

func parseUnix(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (c *Ctl) Has(ctx context.Context, name string) bool {
	_, err := tmuxCmd(ctx, "has-session", "-t", name)
	return err == nil
}

func (c *Ctl) CurrentSession(ctx context.Context) string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := tmuxCmd(ctx, "display-message", "-p", "#S")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (c *Ctl) CurrentSessionPath(ctx context.Context) string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := tmuxCmd(ctx, "display-message", "-p", "#{session_path}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CurrentContext returns session name + path in one tmux call (1 fork instead of 2).
func (c *Ctl) CurrentContext(ctx context.Context) (session, path string) {
	if os.Getenv("TMUX") == "" {
		return "", ""
	}
	out, err := tmuxCmd(ctx, "display-message", "-p", "#S\t#{session_path}")
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(out, "\t", 2)
	if len(parts) >= 1 {
		session = strings.TrimSpace(parts[0])
	}
	if len(parts) >= 2 {
		path = strings.TrimSpace(parts[1])
	}
	return session, path
}

func (c *Ctl) Kill(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("kill: empty session name")
	}
	return tmuxRun(ctx, "kill-session", "-t", name)
}

// runChain: one tmux client process, commands separated by "\;".
func (c *Ctl) runChain(ctx context.Context, parts ...[]string) error {
	return tmuxRunChain(ctx, parts...)
}

const freezeFmt = "#{window_index}\t#{window_name}\t#{window_layout}\t#{pane_index}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_start_command}\t#{pane_pid}\t#{pane_active}\t#{session_path}\t#{session_id}\t#{@gotomux_session_id_v1}\t#{window_id}\t#{@gotomux_window_slot_v1}\t#{pane_id}\t#{@gotomux_pane_slot_v1}\t#{socket_path}\t#{pid}"

func (c *Ctl) Freeze(ctx context.Context, name string) (*model.Session, error) {
	if !project.ValidSessionName(name) {
		return nil, fmt.Errorf("invalid session name %q", name)
	}
	if !c.Has(ctx, name) {
		return nil, fmt.Errorf("session %q not found", name)
	}
	raw, err := tmuxCmd(ctx, "list-panes", "-s", "-t", "="+name, "-F", freezeFmt)
	if err != nil {
		return nil, fmt.Errorf("list-panes: %w", err)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return nil, fmt.Errorf("session %q has no panes", name)
	}
	serverPath, serverPID := "", ""
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) != 18 {
			return nil, fmt.Errorf("list-panes: incomplete identity row for session %q: got %d fields", name, len(parts))
		}
		if serverPath == "" {
			serverPath, serverPID = parts[16], parts[17]
			continue
		}
		if parts[16] != serverPath || parts[17] != serverPID {
			return nil, fmt.Errorf("tmux server changed while listing session %q; retry", name)
		}
	}
	serverKey := tmuxServerKey(ctx, name)
	if serverKey == "" || !strings.HasPrefix(serverKey, serverPath+":"+serverPID+":") {
		return nil, fmt.Errorf("tmux server changed while observing session %q; retry", name)
	}

	var procs *procIndex
	needPS := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		for len(parts) < 18 {
			parts = append(parts, "")
		}
		if base := toolclass.Base(parts[5]); base == "" || toolclass.IsShell(base) {
			if base := toolclass.Base(parts[6]); base == "" || toolclass.IsShell(base) {
				needPS = true
				break
			}
		}
	}
	if needPS {
		procs = loadProcIndex()
	}
	type winAcc struct {
		idx    int
		tmuxID string
		slotID string
		name   string
		layout string
		panes  []model.Pane
		cwd    string
	}
	order := []int{}
	byIdx := map[int]*winAcc{}
	sessPath := ""
	sessionID, sessionLogicalID := "", ""

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 14 {
			if len(parts) != 18 {
				return nil, fmt.Errorf("list-panes: incomplete identity row for session %q", name)
			}
		}
		wIdx, _ := strconv.Atoi(parts[0])
		wName := parts[1]
		wLayout := parts[2]
		pIdx, _ := strconv.Atoi(parts[3])
		pPath := parts[4]
		pCur := parts[5]
		pStart := parts[6]
		pPid64, _ := strconv.ParseInt(parts[7], 10, 32)
		pActive := parts[8] == "1"
		if sessPath == "" {
			sessPath = parts[9]
		}
		rowSessionID, rowSessionLogicalID := parts[10], parts[11]
		if sessionID == "" {
			sessionID, sessionLogicalID = rowSessionID, rowSessionLogicalID
		} else if rowSessionID != sessionID || rowSessionLogicalID != sessionLogicalID {
			return nil, fmt.Errorf("session identity changed while listing panes for %q", name)
		}
		windowID, windowSlotID := parts[12], parts[13]
		paneID, paneSlotID := parts[14], parts[15]

		w, ok := byIdx[wIdx]
		if !ok {
			w = &winAcc{idx: wIdx, tmuxID: windowID, slotID: windowSlotID, name: wName, layout: wLayout}
			byIdx[wIdx] = w
			order = append(order, wIdx)
		}
		cmd := detectPaneCmd(pCur, pStart, int32(pPid64), procs)
		cmdPath := resolvePaneCmdPath(cmd, int32(pPid64), procs)
		cmdPath = replayExecutablePath(cmdPath)
		if strings.TrimSpace(pStart) == "" && !inferredReplayTool(cmd) {
			cmd, cmdPath = "", ""
		}
		w.panes = append(w.panes, model.Pane{
			Idx:      pIdx,
			Cwd:      pPath,
			Cmd:      cmd,
			CmdPath:  cmdPath,
			StartCmd: pStart,
			SlotID:   paneSlotID,
			TmuxID:   paneID,
		})
		if w.cwd == "" || pActive {
			if pPath != "" {
				w.cwd = pPath
			}
		}
	}

	sess := &model.Session{Name: name, Cwd: sessPath, TmuxID: sessionID, LogicalID: sessionLogicalID, ServerKey: serverKey, SchemaVersion: model.CurrentBaselineSchemaVersion, ClassifierVersion: classify.WindowRoleVersion}
	for _, wi := range order {
		w := byIdx[wi]
		if w.cwd == "" {
			w.cwd = sess.Cwd
		}
		sess.Windows = append(sess.Windows, model.Window{
			Idx:    w.idx,
			Name:   w.name,
			Cwd:    w.cwd,
			Layout: LayoutForStore(w.layout, len(w.panes)),
			Panes:  w.panes,
			Role:   classify.WindowRole(model.Window{Name: w.name, Panes: w.panes}),
			SlotID: w.slotID,
			TmuxID: w.tmuxID,
		})
	}
	if sess.Cwd == "" && len(sess.Windows) > 0 {
		sess.Cwd = sess.Windows[0].Cwd
	}
	if after := tmuxServerKey(ctx, name); after == "" || after != serverKey {
		return nil, fmt.Errorf("tmux server changed while classifying session %q; retry", name)
	}
	return sess, nil
}

func tmuxServerKey(ctx context.Context, session string) string {
	return tmuxServerKeyTarget(ctx, "="+session)
}

func (c *Ctl) Load(ctx context.Context, sess *model.Session) error {
	if !project.ValidSessionName(sess.Name) {
		return fmt.Errorf("invalid session name %q", sess.Name)
	}
	if c.Has(ctx, sess.Name) {
		return fmt.Errorf("load %q: session already exists; resolve and attach it explicitly", sess.Name)
	}

	sessCwd := sess.Cwd
	if sessCwd == "" {
		var err error
		sessCwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("load %q: cwd: %w", sess.Name, err)
		}
	}
	base := c.windowBaseIndex(ctx)
	wins := normalizeWindows(sess.Windows, sessCwd)
	w0, p0 := wins[0], wins[0].Panes[0]
	ns := []string{"new-session", "-d", "-s", sess.Name, "-c", p0.Cwd, "-P", "-F", "#{session_id}"}
	if safe := safeWindowName(w0.Name, sess.Name); safe != "" {
		ns = append(ns, "-n", safe)
	}
	if cmd := paneCmd(p0); cmd != "" {
		ns = append(ns, cmdArgs(cmd)...)
	}
	createdID, err := tmuxCmd(ctx, ns...)
	if err != nil {
		return fmt.Errorf("load %q: create session: %w", sess.Name, err)
	}
	if !strings.HasPrefix(createdID, "$") {
		return fmt.Errorf("load %q: tmux did not return the created session runtime ID", sess.Name)
	}
	sess.TmuxID = createdID
	createdServerKey := tmuxServerKeyTarget(ctx, createdID)
	if createdServerKey == "" {
		return fmt.Errorf("load %q: created session %s has no verified server generation; left in place for inspection", sess.Name, createdID)
	}
	sess.ServerKey = createdServerKey
	cleanup := func(cause error) error {
		if tmuxServerKeyTarget(ctx, createdID) == createdServerKey {
			_ = tmuxRun(ctx, "kill-session", "-t", createdID)
		}
		return fmt.Errorf("load %q (created session %s): %w", sess.Name, createdID, cause)
	}
	for i := range wins {
		window := &wins[i]
		if i == 0 {
			rows, err := tmuxCmd(ctx, "list-windows", "-t", createdID, "-F", "#{window_id}")
			if err != nil {
				return cleanup(fmt.Errorf("resolve initial window receipt: %w", err))
			}
			window.TmuxID = strings.TrimSpace(strings.Split(rows, "\n")[0])
		} else {
			pane := window.Panes[0]
			args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", fmt.Sprintf("%s:%d", createdID, base+i), "-c", pane.Cwd}
			if safe := safeWindowName(window.Name, sess.Name); safe != "" {
				args = append(args, "-n", safe)
			}
			if cmd := paneCmd(pane); cmd != "" {
				args = append(args, cmdArgs(cmd)...)
			}
			id, err := tmuxCmd(ctx, args...)
			if err != nil {
				return cleanup(fmt.Errorf("create window %d: %w", i, err))
			}
			window.TmuxID = strings.TrimSpace(id)
		}
		if !strings.HasPrefix(window.TmuxID, "@") {
			return cleanup(fmt.Errorf("tmux returned invalid window receipt %q", window.TmuxID))
		}
		if err := tmuxRun(ctx, "set-option", "-w", "-t", window.TmuxID, "automatic-rename", "off"); err != nil {
			return cleanup(err)
		}
		if safe := safeWindowName(window.Name, sess.Name); safe != "" {
			if err := tmuxRun(ctx, "rename-window", "-t", window.TmuxID, safe); err != nil {
				return cleanup(err)
			}
		}
		paneID, err := tmuxCmd(ctx, "list-panes", "-t", window.TmuxID, "-F", "#{pane_id}")
		if err != nil {
			return cleanup(err)
		}
		window.Panes[0].TmuxID = strings.TrimSpace(strings.Split(paneID, "\n")[0])
		for pi := 1; pi < len(window.Panes); pi++ {
			pane := window.Panes[pi]
			args := []string{"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", window.TmuxID, "-h", "-c", pane.Cwd}
			if cmd := paneCmd(pane); cmd != "" {
				args = append(args, cmdArgs(cmd)...)
			}
			id, err := tmuxCmd(ctx, args...)
			if err != nil {
				return cleanup(fmt.Errorf("create pane %d in window %s: %w", pi, window.TmuxID, err))
			}
			window.Panes[pi].TmuxID = strings.TrimSpace(id)
		}
		if window.Layout != "" {
			ids := map[int]string{}
			indexes := map[int]int{}
			for _, pane := range window.Panes {
				ids[pane.Idx] = pane.TmuxID
				indexes[pane.Idx] = pane.Idx
			}
			layout, err := tmuxlayout.RemapPaneBindingsByIndex(window.Layout, ids, indexes)
			if err != nil {
				return cleanup(err)
			}
			if err := tmuxRun(ctx, "select-layout", "-t", window.TmuxID, layout); err != nil {
				return cleanup(err)
			}
		}
	}
	sess.Windows = wins
	return nil
}

func (c *Ctl) windowBaseIndex(ctx context.Context) int {
	out, err := tmuxCmd(ctx, "show-options", "-gv", "base-index")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func sessionTarget(session string) string {
	if strings.HasPrefix(session, "$") {
		return session + ":"
	}
	return "=" + session + ":"
}

func windowTarget(session string, idx int) string {
	if strings.HasPrefix(session, "$") {
		return fmt.Sprintf("%s:%d", session, idx)
	}
	return fmt.Sprintf("=%s:%d", session, idx)
}

func safeWindowName(name, session string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, "/:\\") {
		return ""
	}
	if strings.Count(name, "/") >= 1 {
		return ""
	}
	if session != "" && name == session {
		return ""
	}
	return name
}

// SafeWindowName returns a tmux window name that can be applied without
// turning an absolute path or the session target itself into an ambiguous name.
func SafeWindowName(name, session string) string { return safeWindowName(name, session) }

func normalizeWindows(wins []model.Window, sessCwd string) []model.Window {
	if len(wins) == 0 {
		return []model.Window{{
			Name:  "",
			Cwd:   sessCwd,
			Panes: []model.Pane{{Cwd: sessCwd}},
		}}
	}
	out := make([]model.Window, len(wins))
	for i, w := range wins {
		out[i] = w
		wcwd := w.Cwd
		if wcwd == "" {
			wcwd = sessCwd
		}
		out[i].Cwd = wcwd
		if len(w.Panes) == 0 {
			out[i].Panes = []model.Pane{{Cwd: wcwd}}
			continue
		}
		panes := make([]model.Pane, len(w.Panes))
		for j, pn := range w.Panes {
			panes[j] = pn
			if panes[j].Cwd == "" {
				panes[j].Cwd = wcwd
			}
		}
		out[i].Panes = panes
	}
	return out
}

func cmdArgs(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	parser := shellwords.NewParser()
	args, err := parser.Parse(cmd)
	if err != nil || parser.Position >= 0 {
		return []string{cmd}
	}
	return args
}

// execAttach replaces gotomux with an interactive tmux client while suppressing
// only tmux's post-detach receipt. An attached tmux client transfers stdin's TTY
// fd to the server, which renders through that fd; after detach, the client
// prints "[detached ...]\n" separately on stdout. Redirecting stdout therefore
// removes the receipt and its cursor-moving newline without touching TTY output.
// Keep a close-on-exec copy so an exec failure can restore diagnostics.
func execAttach(name string) error {
	server, err := newTmuxServer()
	if err != nil {
		return err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd, err := server.PrepareAttachTarget(context.Background(), name, gotmux.TerminalStreams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, gotmux.AttachOptions{})
	if err != nil {
		return err
	}
	// tmux renders through the terminal supplied on stdin. Stdout carries the
	// detach receipt, so suppress it while preserving the terminal stream.
	cmd.Stdout = devNull
	return cmd.Run()
}

func (c *Ctl) Connect(ctx context.Context, name, cwd string) error {
	if !project.ValidSessionName(name) {
		return fmt.Errorf("invalid session name %q", name)
	}
	if !c.Has(ctx, name) {
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("connect %q: cwd: %w", name, err)
			}
		}
		if err := tmuxRun(ctx, "new-session", "-d", "-s", name, "-c", cwd); err != nil {
			return fmt.Errorf("create session %q: %w", name, err)
		}
	}
	if os.Getenv("TMUX") != "" {
		if err := tmuxRun(ctx, "switch-client", "-t", name); err != nil {
			return fmt.Errorf("switch to %q: %w", name, err)
		}
		return nil
	}
	// Replace gotomux with the interactive client. Its stdout is suppressed by
	// execAttach, while the server continues to render through stdin's TTY fd.
	if err := execAttach(name); err != nil {
		return fmt.Errorf("attach to %q: %w", name, err)
	}
	return nil
}

// ConnectObserved attaches to the exact live session selected from one
// server-scoped observation. It never creates a session and never resolves by
// mutable session name.
func (c *Ctl) ConnectObserved(ctx context.Context, runtimeID, serverKey string) error {
	if err := c.ValidateObserved(ctx, runtimeID, serverKey); err != nil {
		return err
	}
	if os.Getenv("TMUX") != "" {
		// TMUX/TMUX_PANE identify the invoking client context. Let tmux resolve
		// that client; gotomux supplies the exact, already validated runtime ID.
		return switchObservedClient(ctx, runtimeID)
	}
	return execAttach(runtimeID)
}

func switchObservedClient(ctx context.Context, runtimeID string) error {
	return tmuxRun(ctx, "switch-client", "-t", runtimeID)
}

func (c *Ctl) ValidateObserved(ctx context.Context, runtimeID, serverKey string) error {
	if !strings.HasPrefix(runtimeID, "$") || serverKey == "" {
		return fmt.Errorf("connect observed session: missing runtime identity or server generation")
	}
	currentKey := tmuxServerKeyTarget(ctx, runtimeID)
	if currentKey == "" || currentKey != serverKey {
		return fmt.Errorf("connect observed session %s: tmux server generation changed; refresh the picker", runtimeID)
	}
	currentID, err := tmuxCmd(ctx, "display-message", "-p", "-t", runtimeID, "#{session_id}")
	if err != nil || strings.TrimSpace(currentID) != runtimeID {
		return fmt.Errorf("connect observed session %s: runtime identity is no longer present", runtimeID)
	}
	return nil
}

func tmuxServerKeyTarget(ctx context.Context, target string) string {
	identity, err := tmuxCmd(ctx, "display-message", "-p", "-t", target, "#{socket_path}	#{pid}	#{start_time}")
	if err != nil || identity == "" {
		return ""
	}
	fields := strings.Split(identity, "	")
	if len(fields) != 3 {
		return ""
	}
	return ServerKeyFromMetadata(fields[0], fields[1], fields[2])
}

// ServerKeyFromMetadata converts gotmux's verified server identity into the
// stable cache key shared by standalone and control-mode observations.
func ServerKeyFromMetadata(socket, serverPID, started string) string {
	if socket == "" || serverPID == "" || started == "" {
		return ""
	}
	return strings.Join([]string{socket, serverPID, started}, ":")
}

func (c *Ctl) RemoveCreatedSession(ctx context.Context, runtimeID, serverKey string) error {
	if err := validateIdentityTarget(IdentitySession, runtimeID); err != nil {
		return err
	}
	if serverKey == "" || tmuxServerKeyTarget(ctx, runtimeID) != serverKey {
		return fmt.Errorf("created session server generation changed; refusing cleanup")
	}
	return tmuxRun(ctx, "kill-session", "-t", runtimeID)
}

func (c *Ctl) KillObserved(ctx context.Context, runtimeID, serverKey string) error {
	if err := c.ValidateObserved(ctx, runtimeID, serverKey); err != nil {
		return err
	}
	return tmuxRun(ctx, "kill-session", "-t", runtimeID)
}
