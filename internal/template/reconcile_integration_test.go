package template

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
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
	if report != "already aligned" {
		t.Errorf("third run report = %q, want explicit already-aligned result", report)
	}
}

func TestNeedsAttentionNoopJournalReplansAfterWindowDisappears(t *testing.T) {
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
	preset := &model.Session{Name: "stale-noop", Cwd: "/tmp", Windows: []model.Window{
		{Name: "editor", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 340", Cwd: "/tmp"}}},
		{Name: "command", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 341", Cwd: "/tmp"}}},
	}}
	if err := ctl.Load(context.Background(), preset); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, _, err := FreezeRemember(ctl, st, preset.Name); err != nil {
		t.Fatal(err)
	}
	baseline, err := st.GetBaseline(preset.Name)
	if err != nil || baseline == nil {
		t.Fatalf("frozen baseline = %+v err=%v", baseline, err)
	}
	snapshot, err := ctl.Observe(context.Background(), preset.Name)
	if err != nil {
		t.Fatal(err)
	}
	desired := core.DesiredState{Baseline: model.BaselineFromSession(*baseline), ClassifierVersion: baseline.ClassifierVersion}
	plan := core.Plan(desired, *snapshot, core.Resolve(desired, *snapshot), core.PolicyPreserve)
	if plan.Status != core.PlanNoop {
		t.Fatalf("initial no-op plan = %+v", plan)
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	journal := model.ReconcileJournal{ID: "stale-noop", SessionKey: baseline.LogicalID, SessionName: baseline.Name, SessionLogicalID: baseline.LogicalID, Policy: string(core.PolicyPreserve), Status: model.JournalPlanned, PlanJSON: string(planJSON)}
	if err := st.StartReconcile(journal); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateReconcile(journal.ID, model.JournalNeedsAttention, 0, "simulated verification failure after noop"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.KillWindowAt(context.Background(), snapshot.TargetSessionID, baseline.Windows[1].TmuxID, baseline.Windows[1].Idx); err != nil {
		t.Fatal(err)
	}
	if report, err := ReconcileSession(context.Background(), ctl, st, preset.Name, false); err != nil || !strings.Contains(report, "1 recreated") {
		t.Fatalf("reconcile after stale noop journal = report %q err=%v", report, err)
	}
	old, err := st.GetReconcile(journal.ID)
	if err != nil || old == nil || old.Status != model.JournalAborted {
		t.Fatalf("stale noop journal = %+v err=%v, want aborted", old, err)
	}
	live, err := ctl.Observe(context.Background(), preset.Name)
	if err != nil || len(live.Target.Windows) != 2 {
		t.Fatalf("live session after replanning = %+v err=%v; want both baseline windows", live, err)
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

func TestSoftReconcileRestoresPaneSlotsToTheirFrozenRows(t *testing.T) {
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
	base := &model.Session{Name: "pane-rows", Cwd: "/tmp", Windows: []model.Window{{
		Name: "command", Cwd: "/tmp", Layout: "tiled", Panes: []model.Pane{
			{Cwd: "/tmp", Cmd: "sleep 320"}, {Cwd: "/tmp", Cmd: "sleep 321"},
			{Cwd: "/tmp", Cmd: "sleep 322"}, {Cwd: "/tmp", Cmd: "sleep 323"},
		},
	}}}
	if err := ctl.Load(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	if _, _, err := FreezeRemember(ctl, st, base.Name); err != nil {
		t.Fatal(err)
	}
	frozen, err := st.GetBaseline(base.Name)
	if err != nil || frozen == nil || len(frozen.Windows) != 1 || len(frozen.Windows[0].Panes) != 4 {
		t.Fatalf("frozen pane baseline = %+v, err=%v", frozen, err)
	}
	windowID := frozen.Windows[0].TmuxID
	initialTops := paneTops(t, windowID)
	type panePosition struct {
		slot string
		top  int
		id   string
	}
	positions := make([]panePosition, 0, len(frozen.Windows[0].Panes))
	for _, pane := range frozen.Windows[0].Panes {
		top, ok := initialTops[pane.TmuxID]
		if !ok {
			t.Fatalf("baseline pane %s missing from initial geometry %v", pane.TmuxID, initialTops)
		}
		positions = append(positions, panePosition{slot: pane.SlotID, top: top, id: pane.TmuxID})
	}
	if positions[0].top != positions[1].top || positions[2].top != positions[3].top || positions[0].top == positions[2].top {
		t.Fatalf("tiled layout did not produce two pane rows: %+v", positions)
	}
	topRow := positions[0].top
	bottomRow := positions[0].top
	for _, pane := range positions[1:] {
		if pane.top < topRow {
			topRow = pane.top
		}
		if pane.top > bottomRow {
			bottomRow = pane.top
		}
	}
	if topRow == bottomRow {
		t.Fatalf("tiled layout has only one pane row: %+v", positions)
	}
	var survivors, missing []panePosition
	for _, pane := range positions {
		if pane.top == topRow {
			missing = append(missing, pane)
		} else {
			survivors = append(survivors, pane)
		}
	}
	if len(survivors) != 2 || len(missing) != 2 {
		t.Fatalf("wanted two top-row panes to remove, survivors=%+v missing=%+v", survivors, missing)
	}
	for _, pane := range missing {
		tmuxOut(t, "kill-pane", "-t", pane.id)
	}
	if _, err := ReconcileSession(context.Background(), ctl, st, base.Name, false); err != nil {
		current, observeErr := ctl.Freeze(context.Background(), base.Name)
		layout := tmuxOut(t, "display-message", "-p", "-t", windowID, "#{window_layout}")
		t.Fatalf("reconcile: %v (observe err=%v, panes=%+v layout=%s)", err, observeErr, current.Windows[0].Panes, layout)
	}
	final, err := ctl.Freeze(context.Background(), base.Name)
	if err != nil {
		t.Fatal(err)
	}
	finalTops := paneTops(t, windowID)
	topsBySlot := make(map[string]int, len(final.Windows[0].Panes))
	for _, pane := range final.Windows[0].Panes {
		top, ok := finalTops[pane.TmuxID]
		if !ok {
			t.Fatalf("final pane %s missing from geometry %v", pane.TmuxID, finalTops)
		}
		topsBySlot[pane.SlotID] = top
	}
	for _, pane := range survivors {
		if got := topsBySlot[pane.slot]; got != bottomRow {
			t.Fatalf("surviving lower-row pane slot %s moved from top=%d to top=%d; geometry=%v", pane.slot, bottomRow, got, topsBySlot)
		}
	}
	for _, pane := range missing {
		if got := topsBySlot[pane.slot]; got >= bottomRow {
			t.Fatalf("restored top-row pane slot %s landed at top=%d, want above surviving row top=%d; geometry=%v", pane.slot, got, bottomRow, topsBySlot)
		}
	}
	if _, err := ReconcileSession(context.Background(), ctl, st, base.Name, false); err != nil {
		t.Fatalf("second reconcile after runtime binding refresh: %v", err)
	}
	second, err := ctl.Freeze(context.Background(), base.Name)
	if err != nil {
		t.Fatal(err)
	}
	secondTops := paneTops(t, windowID)
	for _, pane := range second.Windows[0].Panes {
		if secondTops[pane.TmuxID] != topsBySlot[pane.SlotID] {
			t.Fatalf("pane slot %s moved on repeated reconcile: first top=%d second top=%d", pane.SlotID, topsBySlot[pane.SlotID], secondTops[pane.TmuxID])
		}
	}
}

func paneTops(t *testing.T, windowID string) map[string]int {
	t.Helper()
	lines := strings.Split(tmuxOut(t, "list-panes", "-t", windowID, "-F", "#{pane_id}|#{pane_top}"), "\n")
	tops := make(map[string]int, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid pane geometry row %q", line)
		}
		top, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("invalid pane top in %q: %v", line, err)
		}
		tops[parts[0]] = top
	}
	return tops
}

func TestCodexNoDuplicate(t *testing.T) {
	root := tmuxtest.Isolate(t)
	st, err := store.OpenWithConfig(&config.Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctl, _ := tmux.New()
	name := "kho-cong-reconcile"
	preset := &model.Session{Name: name, Cwd: "/tmp", Windows: []model.Window{
		{Name: "editor", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 300", Cwd: "/tmp"}}},
		{Name: "command", Cwd: "/tmp", Layout: "even-horizontal", Panes: []model.Pane{{Cmd: "sleep 301", Cwd: "/tmp"}, {Cmd: "sleep 302", Cwd: "/tmp"}}},
		{Name: "files", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 303", Cwd: "/tmp"}}},
		{Name: "agent", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 304", Cwd: "/tmp"}}},
	}}
	if err := ctl.Load(context.Background(), preset); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	frozen, err := EnsureLiveIdentities(context.Background(), ctl, name)
	if err != nil {
		t.Fatal(err)
	}
	for wi := range frozen.Windows {
		if frozen.Windows[wi].Name == "agent" {
			// Model the frozen OMP slot while keeping a long-running process in tmux.
			frozen.Windows[wi].Role = "agent"
			frozen.Windows[wi].Panes[0].Cmd = "omp"
			frozen.Windows[wi].Panes[0].CmdPath = ""
			frozen.Windows[wi].Panes[0].StartCmd = "sleep 304"
		}
	}
	if _, _, err := FreezeSave(st, frozen, false); err != nil {
		t.Fatalf("save baseline: %v", err)
	}
	baseline, err := st.GetBaseline(name)
	if err != nil || baseline == nil || len(baseline.Windows) != 4 {
		t.Fatalf("baseline = %+v err=%v", baseline, err)
	}
	var files, agent model.Window
	for _, window := range baseline.Windows {
		switch window.Name {
		case "files":
			files = window
		case "agent":
			agent = window
		}
	}
	if files.TmuxID == "" || agent.TmuxID == "" {
		t.Fatalf("baseline runtime identities missing: files=%+v agent=%+v", files, agent)
	}
	tmuxOut(t, "rename-window", "-t", files.TmuxID, "yazi")
	preservePID := tmuxOut(t, "display-message", "-p", "-t", files.TmuxID, "#{pane_pid}")
	codexIndex := 10
	if err := exec.Command("tmux", "kill-window", "-t", agent.TmuxID).Run(); err != nil {
		t.Fatalf("close agent window: %v", err)
	}
	codexID := tmuxOut(t, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "="+name+":"+strconv.Itoa(codexIndex), "-n", "codex", "-c", "/tmp", "sleep", "305")
	tmuxOut(t, "set-option", "-w", "-t", codexID, "automatic-rename", "off")
	tmuxOut(t, "rename-window", "-t", codexID, "codex")
	tmuxOut(t, "select-window", "-t", files.TmuxID)

	firstReport, err := ReconcileSession(context.Background(), ctl, st, name, false)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if !strings.Contains(firstReport, "1 recreated") {
		t.Fatalf("first report = %q; want one recreated baseline window", firstReport)
	}
	if got := tmuxOut(t, "display-message", "-p", "-t", files.TmuxID, "#{pane_pid}"); got != preservePID {
		t.Fatalf("files pane process changed: got %s want %s", got, preservePID)
	}
	secondReport, err := ReconcileSession(context.Background(), ctl, st, name, false)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !strings.Contains(secondReport, "already aligned") || !strings.Contains(secondReport, "preserved 1 extra window") {
		t.Fatalf("second report = %q; want no-op with Codex preserved", secondReport)
	}
	if got := tmuxOut(t, "list-windows", "-t", "="+name, "-F", "#{window_name}"); strings.Count(got, "agent") != 1 || strings.Count(got, "codex") != 1 {
		t.Fatalf("window names after two reconciles = %q; want one agent and one codex", got)
	}
	if got := tmuxOut(t, "display-message", "-p", "-t", codexID, "#{window_id}"); got != codexID {
		t.Fatalf("Codex runtime window changed: got %q want %q", got, codexID)
	}
}

func TestReconcileLooksUpBaselineBySessionLogicalIDAfterRename(t *testing.T) {
	root := tmuxtest.Isolate(t)
	st, err := store.OpenWithConfig(&config.Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctl, _ := tmux.New()
	base := &model.Session{Name: "rename-old", Cwd: "/tmp", Windows: []model.Window{{Name: "editor", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 300", Cwd: "/tmp"}}}}}
	if err := ctl.Load(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FreezeRemember(ctl, st, base.Name); err != nil {
		t.Fatal(err)
	}
	frozen, err := st.GetBaseline(base.Name)
	if err != nil || frozen == nil || frozen.LogicalID == "" {
		t.Fatalf("baseline identity = %+v err=%v", frozen, err)
	}
	tmuxOut(t, "rename-session", "-t", base.Name, "rename-new")
	report, err := ReconcileSession(context.Background(), ctl, st, "rename-new", false)
	if err != nil {
		t.Fatalf("reconcile renamed session: %v", err)
	}
	if report != "already aligned" {
		t.Fatalf("aligned renamed session report = %q", report)
	}
	if old, err := st.GetBaseline("rename-old"); err != nil || old != nil {
		t.Fatalf("old baseline name lookup = %+v err=%v", old, err)
	}
	renamed, err := st.GetBaselineByLogicalID(frozen.LogicalID)
	if err != nil || renamed == nil || renamed.Name != "rename-new" {
		t.Fatalf("logical baseline after rename = %+v err=%v", renamed, err)
	}
}

func TestUnmanagedEquivalentWindowsKeepProcessesAndDoNotClaimSlots(t *testing.T) {
	root := tmuxtest.Isolate(t)
	st, err := store.OpenWithConfig(&config.Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctl, _ := tmux.New()
	name := "untagged-group"
	preset := &model.Session{Name: name, Cwd: "/tmp", Windows: []model.Window{
		{Name: "editor-a", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 300", Cwd: "/tmp"}}},
		{Name: "editor-b", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 301", Cwd: "/tmp"}}},
		{Name: "anchor", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 302", Cwd: "/tmp"}}},
	}}
	if err := ctl.Load(context.Background(), preset); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	frozen, err := EnsureLiveIdentities(context.Background(), ctl, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := FreezeSave(st, frozen, false); err != nil {
		t.Fatal(err)
	}
	var oldEditors []string
	for _, window := range frozen.Windows {
		if strings.HasPrefix(window.Name, "editor") {
			oldEditors = append(oldEditors, window.TmuxID)
		}
	}
	if len(oldEditors) != 2 {
		t.Fatalf("baseline editor IDs = %v", oldEditors)
	}
	for _, id := range oldEditors {
		tmuxOut(t, "kill-window", "-t", id)
	}
	tmuxOut(t, "new-window", "-d", "-t", "="+name+":7", "-n", "new-a", "-c", "/tmp", "sleep", "310")
	tmuxOut(t, "new-window", "-d", "-t", "="+name+":8", "-n", "new-b", "-c", "/tmp", "sleep", "311")
	before := tmuxOut(t, "list-windows", "-t", "="+name, "-F", "#{window_id}|#{window_name}")
	idsBefore, err := ctl.Observe(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ReconcileSession(context.Background(), ctl, st, name, false)
	if err != nil {
		t.Fatalf("group reconcile returned error: %v", err)
	}
	if !strings.Contains(report, "2 recreated") || !strings.Contains(report, "2 extra window") {
		t.Fatalf("equivalent-group report = %q", report)
	}
	after := tmuxOut(t, "list-windows", "-t", "="+name, "-F", "#{window_id}|#{window_name}")
	if len(strings.Split(before, "\n"))+2 != len(strings.Split(after, "\n")) {
		t.Fatalf("equivalent-group reconciliation changed window count:\nbefore=%s\nafter=%s", before, after)
	}
	idsAfter, err := ctl.Observe(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	beforeIDs, afterIDs := map[string]bool{}, map[string]bool{}
	for _, window := range idsBefore.Target.Windows {
		beforeIDs[window.TmuxID] = true
	}
	for _, window := range idsAfter.Target.Windows {
		afterIDs[window.TmuxID] = true
		if beforeIDs[window.TmuxID] && strings.HasPrefix(window.Name, "new-") && window.SlotID != "" {
			t.Fatalf("unmanaged window %s was assigned baseline ownership", window.TmuxID)
		}
	}
	if len(beforeIDs)+2 != len(afterIDs) {
		t.Fatalf("window runtime identities changed: before=%v after=%v", beforeIDs, afterIDs)
	}
	for id := range beforeIDs {
		if !afterIDs[id] {
			t.Fatalf("live process window %s was replaced while resolving the untagged group", id)
		}
	}
	second, err := ReconcileSession(context.Background(), ctl, st, name, false)
	if err != nil {
		t.Fatalf("second grouped reconcile: %v", err)
	}
	if !strings.Contains(second, "already aligned") || len(strings.Split(tmuxOut(t, "list-windows", "-t", "="+name, "-F", "#{window_id}"), "\n")) != len(afterIDs) {
		t.Fatalf("second reconcile was not idempotent: report=%q", second)
	}
}

type detachedConnector struct {
	*tmux.Ctl
	connectedID string
}

func (c *detachedConnector) ConnectObserved(ctx context.Context, id, key string) error {
	snapshot, err := c.Observe(ctx, "recreate-contract")
	if err != nil {
		return err
	}
	if snapshot.TargetSessionID != id || snapshot.ServerKey != key {
		return fmt.Errorf("attach receipt mismatch")
	}
	c.connectedID = id
	return nil
}

func TestPresetRecreationOnSurvivingServerRetainsFrozenContract(t *testing.T) {
	root := tmuxtest.Isolate(t)
	// Keep the tmux server alive after removing the target incarnation.
	tmuxOut(t, "new-session", "-d", "-s", "recreate-holder")
	st, err := store.OpenWithConfig(&config.Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctl, _ := tmux.New()
	recipe := &model.Session{Name: "recreate-contract", Cwd: "/tmp", Windows: []model.Window{{Name: "worker", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "sleep 300", Cwd: "/tmp"}}}}}
	if err := ctl.Load(context.Background(), recipe); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FreezeRemember(ctl, st, recipe.Name); err != nil {
		t.Fatal(err)
	}
	frozen, err := st.GetBaseline(recipe.Name)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := st.Get(recipe.Name)
	if err != nil {
		t.Fatal(err)
	}
	oldRuntime := frozen.TmuxID
	tmuxOut(t, "kill-session", "-t", oldRuntime)
	detached := &detachedConnector{Ctl: ctl}
	if err := ConnectPreset(detached, st, saved); err != nil {
		t.Fatal(err)
	}
	if detached.connectedID == oldRuntime || detached.connectedID == "" {
		t.Fatal("recreate did not bind new incarnation")
	}
	afterLoad, err := st.GetBaseline(recipe.Name)
	if err != nil {
		t.Fatal(err)
	}
	if afterLoad.TmuxID != oldRuntime || afterLoad.LogicalID != frozen.LogicalID {
		t.Fatal("load rewrote frozen contract")
	}
	if _, err := ReconcileSession(context.Background(), ctl, st, recipe.Name, false); err != nil {
		t.Fatalf("recreated logical instance cannot reconcile: %v", err)
	}
	rebound, err := st.GetBaseline(recipe.Name)
	if err != nil {
		t.Fatal(err)
	}
	if rebound.LogicalID != frozen.LogicalID || rebound.TmuxID != detached.connectedID {
		t.Fatal("verified reconcile did not refresh runtime binding")
	}
	if report, err := ReconcileSession(context.Background(), ctl, st, recipe.Name, false); err != nil || !strings.Contains(report, "already aligned") {
		t.Fatalf("repeat is not idempotent: %s %v", report, err)
	}
}
