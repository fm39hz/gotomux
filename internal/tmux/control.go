package tmux

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ControlConn is a tmux -C control mode subprocess.
// Single persistent process — one exec at init, then pipe commands via stdin,
// read responses (with %begin/%end markers) from stdout.
// No per-command fork overhead.
type ControlConn struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	readTimeout time.Duration
}

// StartControl spawns "tmux -C" and returns a ControlConn.
// Requires the tmux server to be running first (call start-server / ensureServer).
func StartControl() (*ControlConn, error) {
	cmd := exec.Command("tmux", "-C")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("tmux -C stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("tmux -C stdout: %w", err)
	}
	// stderr: discard to avoid blocking; tmux errors come via %error on stdout
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tmux -C start: %w", err)
	}

	return &ControlConn{
		cmd: cmd, in: in, out: bufio.NewReader(out),
		readTimeout: 5 * time.Second,
	}, nil
}

func buildCommand(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if strings.ContainsAny(a, " '\";") {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(a, "'", "'\\''"))
			b.WriteByte('\'')
		} else {
			b.WriteString(a)
		}
	}
	b.WriteByte('\n')
	return b.String()
}

// Send writes a command to the tmux -C subprocess and reads the response.
// Read deadline via goroutine + timeout channel (no SetReadDeadline on pipe).
func (cc *ControlConn) Send(ctx context.Context, args ...string) (string, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.cmd == nil {
		return "", fmt.Errorf("tmux: not connected")
	}

	cmdLine := buildCommand(args)
	if _, err := fmt.Fprint(cc.in, cmdLine); err != nil {
		cc.closeLocked()
		return "", fmt.Errorf("tmux send: %w", err)
	}

	return cc.readResponse(ctx)
}

func (cc *ControlConn) readResponse(ctx context.Context) (string, error) {
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var out strings.Builder
		for {
			line, err := cc.out.ReadString('\n')
			if err != nil {
				cc.closeLocked()
				ch <- res{"", fmt.Errorf("tmux recv: %w", err)}
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case strings.HasPrefix(line, "%begin "):
			case strings.HasPrefix(line, "%end "):
				ch <- res{strings.TrimRight(out.String(), "\n"), nil}
				return
			case strings.HasPrefix(line, "%error "):
				ch <- res{"", fmt.Errorf("tmux: %s", strings.TrimSpace(line[7:]))}
				return
			default:
				if strings.HasPrefix(line, "%") {
					continue
				}
				out.WriteString(line)
				out.WriteByte('\n')
			}
		}
	}()

	t := cc.readTimeout
	if d, ok := ctx.Deadline(); ok {
		remaining := time.Until(d)
		if remaining < t {
			t = remaining
		}
	}
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(t):
		cc.closeLocked()
		return "", fmt.Errorf("tmux: response timeout (%v)", t)
	case <-ctx.Done():
		cc.closeLocked()
		return "", ctx.Err()
	}
}

func (cc *ControlConn) Close() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.closeLocked()
}

func (cc *ControlConn) closeLocked() {
	if cc.cmd != nil && cc.cmd.Process != nil {
		cc.cmd.Process.Kill()
	}
	cc.in.Close()
	cc.cmd = nil
	cc.in = nil
	cc.out = nil
}

// Reconnect kills the old subprocess and spawns a new one.
func (cc *ControlConn) Reconnect() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	cc.closeLocked()

	cmd := exec.Command("tmux", "-C")
	in, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("tmux -C stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("tmux -C stdout: %w", err)
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tmux -C start: %w", err)
	}

	cc.cmd = cmd
	cc.in = in
	cc.out = bufio.NewReader(out)
	return nil
}
