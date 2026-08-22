package template

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

func tmuxOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

// TestRestoreSessionRebuildsDeadWindowAndKeepsProcesses is the project's
// integration convention applied to reset: private tmux server + private data
// dir (tmuxtest.Isolate), session created THE way production creates it
// (ctl.Load with a preset — multi-word commands go through cmdArgs as split
// argv, never as a single shell-wrapped template), then the crash/restore
// cycle asserted on real windows and panes.
func TestRestoreSessionRebuildsDeadWindowAndKeepsProcesses(t *testing.T) {
	root := tmuxtest.Isolate(t)
	st, err := store.OpenWithConfig(&config.Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctl, err := tmux.New()
	if err != nil {
		t.Fatal(err)
	}

	// Baseline: three windows, each running a long sleep as a distinct tool.
	// ctl.Load splits "sleep 400" into argv via cmdArgs — no shell wrapper,
	// so the panes stay alive just like in load_test.
	p := &model.Session{
		Name: "rst",
		Cwd:  "/tmp",
		Windows: []model.Window{
			{Name: "editor", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 300"}}},
			{Name: "test", Cwd: "/tmp", Panes: []model.Pane{{Cmd: ""}}},
			{Name: "dev", Cwd: "/tmp", Panes: []model.Pane{{Cmd: ""}}},
		},
	}
	if err := ctl.Load(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	tmuxOut(t, "select-window", "-t", "rst:2")
	time.Sleep(600 * time.Millisecond) // let the fresh shells finish their init (zoxide etc.) so the baseline is stable

	// First run adopts the live layout as the baseline (none recorded yet).
	report, err := RestoreSession(context.Background(), ctl, st, "rst")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "adopted") {
		t.Fatalf("first run report = %q, want adoption", report)
	}

	// The "crash": window 1's process exits; tmux closes the window and
	// renumbers 2->1, 3->2 (renumber-windows on, as in ~/.tmux.conf).
	// Capture survivor PIDs first — restore must not disturb them.
	pid2 := tmuxOut(t, "display-message", "-p", "-t", "rst:2", "#{pane_pid}")
	pid3 := tmuxOut(t, "display-message", "-p", "-t", "rst:3", "#{pane_pid}")
	tmuxOut(t, "kill-window", "-t", "rst:1")
	if got := tmuxOut(t, "list-windows", "-t", "rst", "-F", "#{window_index}"); got != "1\n2" {
		t.Fatalf("after crash windows = %q, want 1\\n2", got)
	}

	report, err = RestoreSession(context.Background(), ctl, st, "rst")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "recreated") {
		t.Fatalf("second run report = %q, want a recreate", report)
	}
	time.Sleep(300 * time.Millisecond) // recreated pane needs a beat to exec its command
	if got := tmuxOut(t, "list-windows", "-t", "rst", "-F", "#{window_index}"); got != "1\n2\n3" {
		t.Fatalf("after restore windows = %q, want 1\\n2\\n3", got)
	}
	if cmd := tmuxOut(t, "display-message", "-p", "-t", "rst:1", "#{pane_current_command}"); cmd != "sleep" {
		t.Errorf("window 1 command = %q, want sleep", cmd)
	}
	if got := tmuxOut(t, "display-message", "-p", "-t", "rst:2", "#{pane_pid}"); got != pid2 {
		t.Errorf("window 2 pid = %s, want %s (process must not be disturbed)", got, pid2)
	}
	if got := tmuxOut(t, "display-message", "-p", "-t", "rst:3", "#{pane_pid}"); got != pid3 {
		t.Errorf("window 3 pid = %s, want %s (process must not be disturbed)", got, pid3)
	}

	// The user's active window stays active (current window is per-client, so
	// read it via list-windows' window_active flag instead of display-message).
	cur := ""
	for _, line := range strings.Split(tmuxOut(t, "list-windows", "-t", "rst", "-F", "#{window_active}:#{window_index}"), "\n") {
		if strings.HasPrefix(line, "1:") {
			cur = strings.TrimPrefix(line, "1:")
			break
		}
	}
	if cur != "2" {
		t.Errorf("active window = %q, want 2", cur)
	}

	// Idempotent: running again on the restored session is a no-op.
	report, err = RestoreSession(context.Background(), ctl, st, "rst")
	if err != nil {
		t.Fatal(err)
	}
	if report != "" {
		t.Errorf("third run report = %q, want empty (already at baseline)", report)
	}
}
