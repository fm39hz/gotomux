package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
)

// HiddenControlSession is the session owned by the daemon's control client.
const HiddenControlSession = "__gotomuxd"

func IsHiddenSession(name string) bool { return name == HiddenControlSession }

func DropHidden(in []LiveSession) []LiveSession {
	out := make([]LiveSession, 0, len(in))
	for _, s := range in {
		if !IsHiddenSession(s.Name) {
			out = append(out, s)
		}
	}
	return out
}

var (
	ErrControlClosed = errors.New("tmux control: closed")
	ErrControlDead   = errors.New("tmux control: client exited")
)

const (
	controlReplyTimeout = 5 * time.Second
	controlEventBuf     = 64
)

// ControlConn wraps gotmux's control transport and exposes the existing gotomux API.
type ControlConn struct {
	sendMu   sync.Mutex
	mu       sync.Mutex
	conn     *gotmux.Connection
	cancel   context.CancelFunc
	closed   bool
	dead     bool
	events   chan string
	timeouts atomic.Int64
}

func NewControl() *ControlConn { return &ControlConn{events: make(chan string, controlEventBuf)} }

// ServerRunning checks the server with an existing-only query; it never starts one.
func ServerRunning() bool {
	s, err := newTmuxServer()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlReplyTimeout)
	defer cancel()
	_, err = s.Probe(ctx)
	return err == nil
}

func StartControl() (*ControlConn, error) {
	cc := NewControl()
	if err := cc.Reconnect(); err != nil {
		return nil, err
	}
	return cc, nil
}

func (cc *ControlConn) Events() <-chan string { return cc.events }
func (cc *ControlConn) Timeouts() int64       { return cc.timeouts.Load() }

func (cc *ControlConn) Alive() bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.closed || cc.conn == nil || cc.dead {
		return false
	}
	return true
}

// Reconnect attaches only to the reserved hidden session. NewSession's
// ExistingOnly policy is a final guard against accidentally starting tmux.
func (cc *ControlConn) Reconnect() error {
	cc.sendMu.Lock()
	defer cc.sendMu.Unlock()
	cc.mu.Lock()
	if cc.closed {
		cc.mu.Unlock()
		return ErrControlClosed
	}
	old, oldCancel := cc.conn, cc.cancel
	cc.conn, cc.cancel = nil, nil
	cc.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	if old != nil {
		_ = old.Close()
	}

	server, err := newTmuxServer()
	if err != nil {
		return fmt.Errorf("tmux control configure: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ss, err := server.Sessions(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("tmux control list sessions: %w", err)
	}
	var hidden gotmux.Session
	for _, s := range ss {
		if s.Name == HiddenControlSession {
			hidden = s.Handle()
			break
		}
	}
	if hidden.ID() == "" {
		hidden, err = server.NewSession(ctx, gotmux.NewSessionOptions{
			Name: HiddenControlSession, Program: gotmux.Exec("cat"), Start: gotmux.ExistingOnly,
		})
		if err != nil {
			cancel()
			return fmt.Errorf("tmux control create hidden session: %w", err)
		}
	}
	conn, err := server.OpenControl(ctx, hidden, gotmux.ControlOptions{PaneOutput: false})
	if err != nil {
		cancel()
		return fmt.Errorf("tmux control attach hidden session: %w", err)
	}
	stream, err := conn.Events(ctx, gotmux.EventOptions{})
	if err != nil {
		cancel()
		_ = conn.Close()
		return fmt.Errorf("tmux control events: %w", err)
	}
	cc.mu.Lock()
	if cc.closed {
		cc.mu.Unlock()
		cancel()
		_ = stream.Close()
		_ = conn.Close()
		return ErrControlClosed
	}
	cc.conn, cc.cancel = conn, cancel
	cc.dead = false
	cc.mu.Unlock()
	go func() {
		_ = conn.Wait(context.Background())
		cc.mu.Lock()
		if cc.conn == conn {
			cc.dead = true
		}
		cc.mu.Unlock()
	}()
	go cc.forwardEvents(ctx, stream)
	return nil
}

func (cc *ControlConn) forwardEvents(ctx context.Context, stream *gotmux.EventStream) {
	defer stream.Close()
	for {
		ev, err := stream.Next(ctx)
		if err != nil {
			return
		}
		line := "%" + ev.RawName()
		select {
		case cc.events <- line:
		default:
		}
	}
}

func (cc *ControlConn) Send(ctx context.Context, args ...string) (string, error) {
	return cc.SendLines(ctx, args)
}

// SendLines runs the picker’s raw list queries through gotmux's command
// executor. The owned long-lived control client remains responsible for events
// and liveness; this avoids reimplementing control framing for query output.
func (cc *ControlConn) SendLines(ctx context.Context, cmds ...[]string) (string, error) {
	if len(cmds) == 0 {
		return "", nil
	}
	cc.sendMu.Lock()
	defer cc.sendMu.Unlock()
	cc.mu.Lock()
	conn, closed := cc.conn, cc.closed
	cc.mu.Unlock()
	if closed {
		return "", ErrControlClosed
	}
	if conn == nil {
		return "", ErrControlDead
	}
	if !cc.Alive() {
		return "", ErrControlDead
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(controlReplyTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	qctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for _, cmd := range cmds {
		if len(cmd) == 0 || (cmd[0] != "list-sessions" && cmd[0] != "list-panes") {
			return "", fmt.Errorf("tmux control: unsupported query %q", strings.Join(cmd, " "))
		}
	}
	output, err := tmuxSequenceOutput(qctx, cmds...)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			cc.timeouts.Add(1)
		}
		return "", err
	}
	return strings.TrimRight(string(output), "\n"), nil
}

func (cc *ControlConn) Close() {
	cc.mu.Lock()
	if cc.closed {
		cc.mu.Unlock()
		return
	}
	cc.closed = true
	conn, cancel := cc.conn, cc.cancel
	cc.conn, cc.cancel = nil, nil
	cc.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
}
