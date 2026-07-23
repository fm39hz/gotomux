package tmux

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ControlConn wraps tmux -C for persistent listing.
type ControlConn struct {
	mu  sync.Mutex
	w   io.WriteCloser
	r   *bufio.Reader
	cmd *exec.Cmd
}

func StartControl() (*ControlConn, error) {
	cmd := exec.Command("tmux", "-C")
	w, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	r, err := cmd.StdoutPipe()
	if err != nil {
		w.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("tmux -C: %w", err)
	}
	return &ControlConn{w: w, r: bufio.NewReader(r), cmd: cmd}, nil
}

// Send writes a command and reads the response synchronously.
// The goroutine-per-call approach was removed because context cancellation
// leaked the reader goroutine AND consumed data meant for the next Send,
// corrupting the protocol stream.
//
// The mutex ensures serial access. tmux -C responds in <1ms for listing,
// so blocking is acceptable. If non-blocking cancellation becomes needed
// later, wrap with a per-Send io.Pipe + copy goroutine.
func (cc *ControlConn) Send(ctx context.Context, args ...string) (string, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	var cmdLine strings.Builder
	for i, a := range args {
		if i > 0 {
			cmdLine.WriteByte(' ')
		}
		if strings.ContainsAny(a, " '\";") {
			cmdLine.WriteByte('\'')
			cmdLine.WriteString(strings.ReplaceAll(a, "'", "'\\''"))
			cmdLine.WriteByte('\'')
		} else {
			cmdLine.WriteString(a)
		}
	}
	cmdLine.WriteByte('\n')

	if _, err := io.WriteString(cc.w, cmdLine.String()); err != nil {
		return "", fmt.Errorf("tmux -C send: %w", err)
	}

	var out strings.Builder
	for {
		line, err := cc.r.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("tmux -C recv: %w", err)
		}
		line = strings.TrimSuffix(line, "\n")

		switch {
		case strings.HasPrefix(line, "%begin "):
		case strings.HasPrefix(line, "%end "):
			return strings.TrimRight(out.String(), "\n"), nil
		case strings.HasPrefix(line, "%error "):
			return "", fmt.Errorf("tmux -C: %s", strings.TrimSpace(line[7:]))
		default:
			if strings.HasPrefix(line, "%") {
				continue
			}
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
}

func (cc *ControlConn) Close() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.w.Close()
	if cc.cmd != nil && cc.cmd.Process != nil {
		cc.cmd.Wait()
	}
}

// Reconnect kills the old tmux -C process and starts a new one.
func (cc *ControlConn) Reconnect() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	cc.w.Close()
	if cc.cmd != nil && cc.cmd.Process != nil {
		cc.cmd.Wait()
	}

	cmd := exec.Command("tmux", "-C")
	w, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	r, err := cmd.StdoutPipe()
	if err != nil {
		w.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return fmt.Errorf("reconnect tmux -C: %w", err)
	}
	cc.w = w
	cc.r = bufio.NewReader(r)
	cc.cmd = cmd
	return nil
}
