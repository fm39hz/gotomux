package tmux

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ControlConn is a direct tmux control socket connection.
// Replaces the old tmux -C subprocess approach — no extra process, no pipe hacks.
// SetReadDeadline on the underlying net.Conn gives clean timeouts without
// goroutine leaks.
type ControlConn struct {
	mu   sync.Mutex
	conn net.Conn
	br   *bufio.Reader
	path string
}

func tmuxSocketPath() (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "#{socket_path}").Output()
	if err != nil {
		return "", fmt.Errorf("resolve tmux socket: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func dialTMUX(timeout time.Duration) (net.Conn, string, error) {
	path, err := tmuxSocketPath()
	if err != nil {
		return nil, "", err
	}
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return nil, "", fmt.Errorf("dial tmux %s: %w", path, err)
	}
	return conn, path, nil
}

// StartControl dials the tmux control socket directly.
// Requires the tmux server to be running (call ensureServer / start-server first).
func StartControl() (*ControlConn, error) {
	conn, path, err := dialTMUX(2 * time.Second)
	if err != nil {
		return nil, err
	}
	return &ControlConn{conn: conn, br: bufio.NewReader(conn), path: path}, nil
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

// Send writes a command to the tmux control socket and reads the response.
// A 5-second deadline prevents hanging. On any I/O error the connection is
// closed; caller must Reconnect before the next call.
func (cc *ControlConn) Send(ctx context.Context, args ...string) (string, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.conn == nil {
		return "", fmt.Errorf("tmux: not connected")
	}

	cmdLine := buildCommand(args)
	dl := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}

	cc.conn.SetWriteDeadline(dl)
	if _, err := fmt.Fprint(cc.conn, cmdLine); err != nil {
		cc.conn.Close()
		cc.conn = nil
		return "", fmt.Errorf("tmux send: %w", err)
	}

	cc.conn.SetReadDeadline(dl)

	var out strings.Builder
	for {
		line, err := cc.br.ReadString('\n')
		if err != nil {
			cc.conn.Close()
			cc.conn = nil
			return "", fmt.Errorf("tmux recv: %w", err)
		}
		line = strings.TrimSuffix(line, "\n")

		switch {
		case strings.HasPrefix(line, "%begin "):
		case strings.HasPrefix(line, "%end "):
			return strings.TrimRight(out.String(), "\n"), nil
		case strings.HasPrefix(line, "%error "):
			return "", fmt.Errorf("tmux: %s", strings.TrimSpace(line[7:]))
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
	if cc.conn != nil {
		cc.conn.Close()
		cc.conn = nil
	}
}

// Reconnect closes the old socket and opens a new connection.
func (cc *ControlConn) Reconnect() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.conn != nil {
		cc.conn.Close()
		cc.conn = nil
	}

	conn, path, err := dialTMUX(2 * time.Second)
	if err != nil {
		return err
	}
	cc.conn = conn
	cc.path = path
	cc.br = bufio.NewReader(conn)
	return nil
}
