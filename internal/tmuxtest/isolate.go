// Package tmuxtest isolates tests from the developer's real tmux server.
//
// It exists because getting this wrong is silently destructive. tmux ignores a
// TMUX_TMPDIR that does not exist — start-server still succeeds and quietly uses
// the default socket — so a test that sets the variable, creates sessions, and
// runs kill-server on cleanup will destroy the developer's live sessions without
// a single error message. That happened during development of this package.
//
// Isolate therefore does two things the obvious version does not: it creates the
// socket directory, and it PROVES the resulting socket lives inside that
// directory before registering any destructive cleanup. If the proof fails it
// aborts without touching the server.
package tmuxtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Isolate points tmux at a private socket directory for the duration of the
// test and returns a scratch root the caller can use for its own state (store,
// sockets, lock files). It skips under -short or when tmux is absent.
func Isolate(t *testing.T) (root string) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: needs a live tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	root = t.TempDir()
	sockDir := filepath.Join(root, "tmuxtmp")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", sockDir, err)
	}
	// TMUX_TMPDIR must exist or tmux falls back to the default socket.
	t.Setenv("TMUX_TMPDIR", sockDir)
	// Behave as if run outside tmux, regardless of how the test was launched.
	t.Setenv("TMUX", "")

	if out, err := exec.Command("tmux", "start-server").CombinedOutput(); err != nil {
		t.Skipf("start-server: %v: %s", err, strings.TrimSpace(string(out)))
	}

	out, err := exec.Command("tmux", "display-message", "-p", "#{socket_path}").Output()
	if err != nil {
		t.Fatalf("resolve socket path: %v", err)
	}
	sock := strings.TrimSpace(string(out))
	if !strings.HasPrefix(sock, sockDir+string(os.PathSeparator)) {
		// Deliberately no cleanup registered: killing this server would kill the
		// real one.
		t.Fatalf("tmux is NOT isolated: socket %q is outside %q — refusing to run, "+
			"because the teardown would kill the developer's real tmux server", sock, sockDir)
	}

	// Only now is teardown safe.
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
	return root
}

// NewSessions creates detached sessions in the isolated server.
func NewSessions(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", n).CombinedOutput(); err != nil {
			t.Fatalf("new-session %s: %v: %s", n, err, strings.TrimSpace(string(out)))
		}
	}
}
