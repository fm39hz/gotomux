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

func waitForPaneCommand(t *testing.T, target, command string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		if got := tmuxOut(t, "display-message", "-p", "-t", target, "#{pane_current_command}"); got == command {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pane %s did not reach command %q", target, command)
}

// TestReconcileSessionRebuildsDeadWindowAndKeepsProcesses is the project's
// integration convention for reconciliation: private tmux server + private data
// dir (tmuxtest.Isolate), session created THE way production creates it
// (ctl.Load with a preset — multi-word commands go through cmdArgs as split
// argv, never as a single shell-wrapped template), then the crash/reconcile
// cycle asserted on real windows and panes.
func TestReconcileSessionRebuildsDeadWindowAndKeepsProcesses(t *testing.T) {
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

	// Reconcile requires an explicit baseline. Freeze the launch layout before
	// simulating the crash, just as a user does with gotomux -f.
	if _, _, err := FreezeRemember(ctl, st, "rst"); err != nil {
		t.Fatal(err)
	}
	baseline, err := st.GetBaseline("rst")
	if err != nil || baseline == nil || baseline.ServerKey == "" {
		t.Fatalf("baseline runtime server binding = %+v, err=%v", baseline, err)
	}
	for _, w := range baseline.Windows {
		if w.TmuxID == "" {
			t.Fatalf("window %q has no tmux identity", w.Name)
		}
		for _, pane := range w.Panes {
			if pane.TmuxID == "" {
				t.Fatalf("window %q pane %d has no tmux identity", w.Name, pane.Idx)
			}
		}
	}

	// The "crash": window 1's process exits; tmux closes the window and
	// renumbers 2->1, 3->2 (renumber-windows on, as in ~/.tmux.conf).
	// Capture survivor PIDs first — soft reconciliation must preserve them.
	pid2 := tmuxOut(t, "display-message", "-p", "-t", "rst:2", "#{pane_pid}")
	pid3 := tmuxOut(t, "display-message", "-p", "-t", "rst:3", "#{pane_pid}")
	tmuxOut(t, "kill-window", "-t", "rst:1")
	if got := tmuxOut(t, "list-windows", "-t", "rst", "-F", "#{window_index}"); got != "1\n2" {
		t.Fatalf("after crash windows = %q, want 1\\n2", got)
	}

	report, err := ReconcileSession(context.Background(), ctl, st, "rst", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "recreated") {
		t.Fatalf("second run report = %q, want a recreate", report)
	}
	waitForPaneCommand(t, "rst:1", "sleep")
	if got := tmuxOut(t, "list-windows", "-t", "rst", "-F", "#{window_index}"); got != "1\n2\n3" {
		t.Fatalf("after reconciliation windows = %q, want 1\\n2\\n3", got)
	}
	if got := tmuxOut(t, "display-message", "-p", "-t", "rst:2", "#{pane_pid}"); got != pid2 {
		t.Errorf("window 2 pid = %s, want %s (process must not be disturbed)", got, pid2)
	}
	if got := tmuxOut(t, "display-message", "-p", "-t", "rst:3", "#{pane_pid}"); got != pid3 {
		t.Errorf("window 3 pid = %s, want %s (process must not be disturbed)", got, pid3)
	}
	baseline, err = st.GetBaseline("rst")
	if err != nil || baseline == nil || baseline.ServerKey == "" || len(baseline.Windows) != 3 {
		t.Fatalf("rebound baseline = %+v, err=%v", baseline, err)
	}
	for _, w := range baseline.Windows {
		if w.TmuxID == "" || len(w.Panes) == 0 || w.Panes[0].TmuxID == "" {
			t.Fatalf("runtime IDs were not refreshed after reconcile: %+v", w)
		}
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

	// Idempotent: running again on the reconciled session is a no-op.
	report, err = ReconcileSession(context.Background(), ctl, st, "rst", false)
	if err != nil {
		t.Fatal(err)
	}
	if report != "" {
		t.Errorf("third run report = %q, want empty (already at baseline)", report)
	}
}

func TestHardReconcileRebuildsFrozenWindowsAndRemovesExtras(t *testing.T) {
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
	base := &model.Session{Name: "hard-test", Cwd: "/tmp", Windows: []model.Window{
		{Name: "editor", Cwd: "/tmp", Panes: []model.Pane{{Cwd: "/tmp", Cmd: "sleep 300"}}},
		{Name: "command", Cwd: "/tmp", Layout: "tiled", Panes: []model.Pane{{Cwd: "/tmp", Cmd: "sleep 301"}, {Cwd: "/tmp", Cmd: "sleep 302"}, {Cwd: "/tmp", Cmd: "sleep 304"}}},
	}}
	if err := ctl.Load(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, _, err := FreezeRemember(ctl, st, base.Name); err != nil {
		t.Fatal(err)
	}
	frozen, err := st.GetBaseline(base.Name)
	if err != nil || frozen == nil || len(frozen.Windows) != 2 {
		t.Fatalf("frozen baseline = %+v, err=%v", frozen, err)
	}
	oldPIDs := strings.Split(tmuxOut(t, "list-panes", "-s", "-t", base.Name, "-F", "#{pane_pid}"), "\n")
	tmuxOut(t, "new-window", "-d", "-t", "=hard-test:9", "-n", "extra", "-c", "/tmp")
	tmuxOut(t, "split-window", "-t", frozen.Windows[1].TmuxID, "-h", "-c", "/tmp", "sleep", "303")

	report, err := ReconcileSession(context.Background(), ctl, st, base.Name, true)
	if err != nil {
		t.Fatalf("hard reconcile: %v (report %q)", err, report)
	}
	if got := tmuxOut(t, "list-windows", "-t", base.Name, "-F", "#{window_name}|#{window_panes}"); got != "editor|1\ncommand|3" {
		t.Fatalf("windows after hard reconcile = %q", got)
	}
	layoutLines := strings.Split(tmuxOut(t, "list-windows", "-t", base.Name, "-F", "#{window_name}|#{window_layout}"), "\n")
	var commandLayout string
	for _, line := range layoutLines {
		if strings.HasPrefix(line, "command|") {
			commandLayout = strings.TrimPrefix(line, "command|")
		}
	}
	if commandLayout == "" || tmux.LayoutTopology(commandLayout) != tmux.LayoutTopology(frozen.Windows[1].Layout) {
		t.Fatalf("hard reconcile layout topology = %q, want baseline %q", commandLayout, frozen.Windows[1].Layout)
	}
	newPIDs := strings.Split(tmuxOut(t, "list-panes", "-s", "-t", base.Name, "-F", "#{pane_pid}"), "\n")
	if len(oldPIDs) != 4 || len(newPIDs) != 4 {
		t.Fatalf("old/new pane counts = %d/%d", len(oldPIDs), len(newPIDs))
	}
	old := map[string]bool{}
	for _, pid := range oldPIDs {
		old[pid] = true
	}
	for _, pid := range newPIDs {
		if old[pid] {
			t.Errorf("pane process %s survived hard reconcile", pid)
		}
	}
	rebound, err := st.GetBaseline(base.Name)
	if err != nil || rebound == nil || rebound.ServerKey == "" {
		t.Fatalf("hard reconcile runtime binding = %+v, err=%v", rebound, err)
	}
	for _, w := range rebound.Windows {
		if w.TmuxID == "" || len(w.Panes) == 0 || w.Panes[0].TmuxID == "" {
			t.Fatalf("hard reconcile did not refresh runtime ids: %+v", w)
		}
	}
}

func TestSoftReconcileRestoresSplitTreeAndPreservesPaneProcesses(t *testing.T) {
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
	p := &model.Session{Name: "soft-layout", Cwd: "/tmp", Windows: []model.Window{{
		Name: "command", Cwd: "/tmp", Layout: "tiled", Panes: []model.Pane{
			{Cwd: "/tmp", Cmd: "sleep 300"},
			{Cwd: "/tmp", Cmd: "sleep 301"},
			{Cwd: "/tmp", Cmd: "sleep 302"},
		},
	}}}
	if err := ctl.Load(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, _, err := FreezeRemember(ctl, st, p.Name); err != nil {
		t.Fatal(err)
	}
	baseline, err := st.GetBaseline(p.Name)
	if err != nil || baseline == nil || baseline.Windows[0].TmuxID == "" {
		t.Fatalf("baseline = %+v, err=%v", baseline, err)
	}
	before := tmuxOut(t, "list-panes", "-t", baseline.Windows[0].TmuxID, "-F", "#{pane_id}|#{pane_pid}")
	tmuxOut(t, "select-layout", "-t", baseline.Windows[0].TmuxID, "even-horizontal")
	report, err := ReconcileSession(context.Background(), ctl, st, p.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "repaired") {
		t.Fatalf("report = %q, want layout repair", report)
	}
	after := tmuxOut(t, "list-panes", "-t", baseline.Windows[0].TmuxID, "-F", "#{pane_id}|#{pane_pid}")
	if before != after {
		t.Fatalf("pane ids/processes changed during soft layout repair:\nbefore %s\nafter  %s", before, after)
	}
	live, err := ctl.Freeze(context.Background(), p.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tmux.LayoutTopology(live.Windows[0].Layout), tmux.LayoutTopology(baseline.Windows[0].Layout); got != want {
		t.Fatalf("split topology after reconcile = %q, want frozen %q", got, want)
	}
}
