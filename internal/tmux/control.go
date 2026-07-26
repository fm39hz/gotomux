package tmux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HiddenControlSession is the session the control-mode client owns.
//
// Control mode requires an attached client, and there is no way to have one
// without a session. Measured on tmux 3.7b: `-C attach-session -t X` sets
// session_attached=1 on X and bumps its session_last_attached and
// session_activity — and `-r` (read-only) does not prevent that, it only blocks
// input. Since LiveSession recency is max(last_attached, activity, created), a
// daemon that attached to a user session would be falsifying the exact data it
// exists to serve. Owning a throwaway session leaves user sessions untouched.
//
// The session IS visible to list-sessions, so every consumer must filter it —
// that is the price of not perturbing anything.
const HiddenControlSession = "__gotomuxd"

// IsHiddenSession reports whether name is the daemon's own control session.
func IsHiddenSession(name string) bool { return name == HiddenControlSession }

var (
	// ErrControlClosed means Close was called; the connection will not come back.
	ErrControlClosed = errors.New("tmux control: closed")
	// ErrControlDead means the subprocess is gone; caller should Reconnect.
	ErrControlDead = errors.New("tmux control: client exited")
)

const (
	controlReplyTimeout = 5 * time.Second
	controlEventBuf     = 64
)

type ctlReply struct {
	out string
	err error
}

// ctlProc is one live `tmux -C` subprocess plus the goroutine reading it.
// Reconnect replaces the whole value instead of mutating fields, so a
// half-initialized connection is not representable.
type ctlProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	replies chan ctlReply
	done    chan struct{} // closed when the reader goroutine returns
	killOne sync.Once
}

func (p *ctlProc) kill() {
	p.killOne.Do(func() {
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		go func() { _ = p.cmd.Wait() }() // reap; reader closes done
	})
}

// ControlConn is a long-lived tmux control-mode client.
//
// It replaces an earlier attempt to speak to tmux's own server socket directly.
// That socket speaks binary imsg and requires a MSG_IDENTIFY handshake, so
// writing newline-terminated text to it produced EOF on the first read, every
// time, forever — the daemon logged an error per poll and served empty payloads
// for its entire life. Control mode is only reachable through a real client.
type ControlConn struct {
	// sendMu serializes exchanges. Control mode replies in order, so exactly one
	// in-flight command keeps reply/command pairing unambiguous, and it lets a
	// multi-command batch read a consistent snapshot.
	sendMu sync.Mutex

	mu     sync.Mutex
	proc   *ctlProc
	closed bool

	// events is stable across reconnects so callers may hold onto it. It is
	// never closed: a reader goroutine may still be writing during teardown.
	events chan string

	timeouts atomic.Int64
}

// StartControl spawns the control-mode client immediately. The tmux server must
// already be running.
func StartControl() (*ControlConn, error) {
	cc := NewControl()
	p, err := cc.spawn()
	if err != nil {
		return nil, err
	}
	cc.proc = p
	return cc, nil
}

// NewControl returns a control connection that is not attached yet. Call
// Reconnect to attach, and only once ServerRunning reports a live server.
//
// This split exists because a long-lived monitor must never be the process that
// brings the tmux server into existence. tmux registers a systemd transient scope
// per pane, and those scopes are parented under whatever unit started the server;
// a server started from gotomuxd.service therefore made every pane of every
// session a child of that service, so `systemctl stop gotomuxd` tore them all
// down. Measured: the journal logged "Stopping tmux child pane N" for each pane
// and the server was left with zero sessions.
func NewControl() *ControlConn {
	return &ControlConn{events: make(chan string, controlEventBuf)}
}

// ServerRunning reports whether a tmux server is already up, without starting
// one. `list-sessions` exits 0 for a live server even with zero sessions, and
// exits non-zero with "no server running" otherwise; it never creates a server.
func ServerRunning() bool {
	return exec.Command("tmux", "list-sessions", "-F", "#{session_id}").Run() == nil
}

// Events yields async notifications (%sessions-changed, %session-renamed, …).
// Full buffer drops rather than blocks: notifications are a hint to resync, so
// losing one only costs latency, never correctness — the periodic poll still runs.
func (cc *ControlConn) Events() <-chan string { return cc.events }

// Timeouts counts reply timeouts, for health reporting.
func (cc *ControlConn) Timeouts() int64 { return cc.timeouts.Load() }

// Alive reports whether a usable subprocess is attached right now.
func (cc *ControlConn) Alive() bool {
	_, err := cc.current()
	return err == nil
}

func (cc *ControlConn) spawn() (*ctlProc, error) {
	// -A reuses the hidden session left behind by a previous daemon.
	// `-- cat` gives the session a no-op foreground command: a real shell would
	// render its prompt and flood the stream with %output.
	cmd := exec.Command("tmux", "-C", "new-session", "-A",
		"-s", HiddenControlSession, "--", "cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("tmux control stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("tmux control stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("tmux control stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tmux control start: %w", err)
	}

	p := &ctlProc{
		cmd:   cmd,
		stdin: stdin,
		// cap 1: at most one exchange is in flight.
		replies: make(chan ctlReply, 1),
		done:    make(chan struct{}),
	}
	go cc.read(p, stdout)
	go logStderr(stderr)

	// Suppress %output for this client. Nothing in the hidden session writes
	// today, but a stray window would otherwise drown the stream.
	if _, err := exchange(context.Background(), p, []string{"refresh-client", "-f", "no-output"}, &cc.timeouts); err != nil {
		p.kill()
		return nil, fmt.Errorf("tmux control handshake: %w", err)
	}
	return p, nil
}

func logStderr(r io.ReadCloser) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			log.Printf("[cc] [WARN] tmux: %s", line)
		}
	}
}

// clientBlock reports whether a %begin/%end/%error line is the reply to a
// command this client sent.
//
// The third field of "%begin <ts> <cmdnum> <flags>" is 1 for commands issued by
// the control client and 0 otherwise — notably the implicit new-session that
// starts the connection emits a flag-0 block. Discriminating on it is what keeps
// that unsolicited block from being handed to the first real Send as its reply.
func clientBlock(line string) bool {
	f := strings.Fields(line)
	return len(f) >= 4 && f[3] != "0"
}

func (cc *ControlConn) read(p *ctlProc, stdout io.ReadCloser) {
	defer close(p.done)
	defer stdout.Close()
	br := bufio.NewReaderSize(stdout, 64*1024)

	var (
		inBlock   bool
		solicited bool
		body      strings.Builder
	)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if inBlock && solicited {
				cc.deliver(p, ctlReply{err: fmt.Errorf("tmux control read: %w", err)})
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")

		// Inside a block every line is payload except the two terminators. Do
		// not treat a '%'-prefixed body line as an event.
		if inBlock {
			switch {
			case strings.HasPrefix(line, "%end "):
				if solicited {
					cc.deliver(p, ctlReply{out: strings.TrimRight(body.String(), "\n")})
				}
				inBlock, solicited = false, false
			case strings.HasPrefix(line, "%error "):
				// %error replaces %end as the terminator; the message itself is
				// in the body. (The old code read it out of the %error line.)
				msg := strings.TrimSpace(body.String())
				if msg == "" {
					msg = "command failed"
				}
				if solicited {
					cc.deliver(p, ctlReply{err: fmt.Errorf("tmux: %s", msg)})
				}
				inBlock, solicited = false, false
			default:
				body.WriteString(line)
				body.WriteByte('\n')
			}
			continue
		}

		switch {
		case strings.HasPrefix(line, "%begin "):
			inBlock, solicited = true, clientBlock(line)
			body.Reset()
		case line == "%exit" || strings.HasPrefix(line, "%exit "):
			// The client is going away (e.g. after a malformed command). Let the
			// reader return so current() reports ErrControlDead and the caller
			// reconnects.
			return
		case strings.HasPrefix(line, "%output"):
			// Suppressed via no-output, but ignore explicitly if it slips in.
		case strings.HasPrefix(line, "%"):
			select {
			case cc.events <- line:
			default:
			}
		}
	}
}

func (cc *ControlConn) deliver(p *ctlProc, r ctlReply) {
	select {
	case p.replies <- r:
	default:
		// No exchange waiting (a timed-out one). Drop it: exchange kills the
		// proc on timeout precisely so a late reply cannot be mispaired.
	}
}

func (cc *ControlConn) current() (*ctlProc, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.closed {
		return nil, ErrControlClosed
	}
	if cc.proc == nil {
		return nil, ErrControlDead
	}
	select {
	case <-cc.proc.done:
		return nil, ErrControlDead
	default:
	}
	return cc.proc, nil
}

// buildCommand renders one command line for control mode.
//
// Every argument that is not a bare token is single-quoted. This is not
// cosmetic: tmux lexes each line itself, and the previous quote set omitted TAB,
// so the tab-separated ListSessFmt was split into argv and only "S" survived as
// the format string. Conversely a ";" separator must never be quoted — a quoted
// ';' is passed to list-sessions as a literal argument, which yields
// "too many arguments", then %error, then %exit, killing the client. There is no
// separator here at all: callers pass one command per call.
func buildCommand(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteArg(a))
	}
	b.WriteByte('\n')
	return b.String()
}

const bareToken = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@+,"

func quoteArg(a string) string {
	if a == "" {
		return "''"
	}
	if strings.IndexFunc(a, func(r rune) bool {
		return !strings.ContainsRune(bareToken, r)
	}) < 0 {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// Send runs one command and returns its output.
func (cc *ControlConn) Send(ctx context.Context, args ...string) (string, error) {
	return cc.SendLines(ctx, args)
}

// SendLines runs several commands in one locked exchange and concatenates their
// output, one %begin/%end block per command.
//
// Commands cannot be combined on a single line with ";" here — see buildCommand.
// Concatenating is safe for the list-sessions + list-panes pair because
// ParseLiveOutput keys off the leading S/P tag and does not care about ordering.
func (cc *ControlConn) SendLines(ctx context.Context, cmds ...[]string) (string, error) {
	if len(cmds) == 0 {
		return "", nil
	}
	cc.sendMu.Lock()
	defer cc.sendMu.Unlock()

	p, err := cc.current()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, args := range cmds {
		out, err := exchange(ctx, p, args, &cc.timeouts)
		if err != nil {
			return "", err
		}
		if out != "" {
			b.WriteString(out)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func exchange(ctx context.Context, p *ctlProc, args []string, timeouts *atomic.Int64) (string, error) {
	if _, err := io.WriteString(p.stdin, buildCommand(args)); err != nil {
		p.kill()
		return "", fmt.Errorf("tmux control write: %w", err)
	}

	timeout := controlReplyTimeout
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d < timeout {
			timeout = d
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-p.replies:
		return r.out, r.err
	case <-p.done:
		return "", ErrControlDead
	case <-ctx.Done():
		p.kill()
		return "", ctx.Err()
	case <-timer.C:
		timeouts.Add(1)
		// Kill rather than carry on: a reply that arrives after we stopped
		// waiting would be handed to the *next* command as its result. Forcing a
		// reconnect keeps reply pairing sound.
		p.kill()
		return "", fmt.Errorf("tmux control: reply timeout after %s", timeout)
	}
}

// Reconnect tears down the current subprocess and spawns a fresh one.
func (cc *ControlConn) Reconnect() error {
	cc.sendMu.Lock()
	defer cc.sendMu.Unlock()

	cc.mu.Lock()
	if cc.closed {
		cc.mu.Unlock()
		return ErrControlClosed
	}
	old := cc.proc
	cc.proc = nil
	cc.mu.Unlock()
	if old != nil {
		old.kill()
	}

	p, err := cc.spawn()
	if err != nil {
		return err
	}
	cc.mu.Lock()
	if cc.closed {
		cc.mu.Unlock()
		p.kill()
		return ErrControlClosed
	}
	cc.proc = p
	cc.mu.Unlock()
	return nil
}

func (cc *ControlConn) Close() {
	cc.mu.Lock()
	if cc.closed {
		cc.mu.Unlock()
		return
	}
	cc.closed = true
	p := cc.proc
	cc.proc = nil
	cc.mu.Unlock()
	if p != nil {
		p.kill()
	}
}
