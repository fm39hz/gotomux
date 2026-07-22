package tmux

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ControlConn wraps tmux -C for persistent listing.
// ListLive chỉ dùng control mode; các commands khác dùng exec.Command.
type ControlConn struct {
	mu sync.Mutex
	w  io.WriteCloser
	r  *bufio.Reader
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
	return &ControlConn{w: w, r: bufio.NewReader(r)}, nil
}

func (cc *ControlConn) Send(args ...string) (string, error) {
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
				continue // async notification, skip
			}
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
}

func (cc *ControlConn) Close() {
	cc.w.Close()
}
