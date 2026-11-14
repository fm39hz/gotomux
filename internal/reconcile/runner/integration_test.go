package runner

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

func isolatedTmuxOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

func TestRunnerCreatesTaggedMissingWindowAndRetryDoesNotDuplicate(t *testing.T) {
	root := tmuxtest.Isolate(t)
	isolatedTmuxOut(t, "set-option", "-g", "renumber-windows", "on")
	st, err := store.OpenWithConfig(&config.Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctl, _ := tmux.New()
	isolatedTmuxOut(t, "new-session", "-d", "-s", "runner-e2e", "-c", "/tmp", "sleep", "300")
	snapshot, err := ctl.Observe(context.Background(), "runner-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetStableID(context.Background(), tmux.IdentitySession, snapshot.Target.TmuxID, "00000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetStableID(context.Background(), tmux.IdentityWindow, snapshot.Target.Windows[0].TmuxID, "00000000-0000-4000-8000-000000000002"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetStableID(context.Background(), tmux.IdentityPane, snapshot.Target.Windows[0].Panes[0].TmuxID, "00000000-0000-4000-8000-000000000003"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ctl.Observe(context.Background(), "runner-e2e")
	if err != nil {
		t.Fatal(err)
	}
	base := snapshot.Target
	base.Windows = append([]model.Window(nil), snapshot.Target.Windows...)
	for i := range base.Windows {
		base.Windows[i].Panes = append([]model.Pane(nil), snapshot.Target.Windows[i].Panes...)
	}
	base.ClassifierVersion = 1
	base.Windows = append(base.Windows, model.Window{
		Idx: 2, Name: "agent", Role: "agent", SlotID: "00000000-0000-4000-8000-000000000004", Cwd: "/tmp",
		Panes: []model.Pane{{Idx: 0, Cmd: "sleep", StartCmd: "sleep 301", Cwd: "/tmp", SlotID: "00000000-0000-4000-8000-000000000005"}},
	})
	desired := core.DesiredState{Session: base, ClassifierVersion: 1}
	resolution := core.Resolve(desired, *snapshot)
	plan := core.Plan(desired, *snapshot, resolution, core.PolicyPreserve)
	if plan.Status != core.PlanReady {
		t.Fatalf("plan status=%s reason=%s resolution=%+v", plan.Status, plan.BlockedReason, resolution)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	journal := model.ReconcileJournal{ID: "run-e2e", SessionKey: base.LogicalID, SessionName: base.Name, SessionLogicalID: base.LogicalID, Policy: string(plan.Policy), Status: model.JournalPlanned, PlanJSON: string(body)}
	if err := st.StartReconcile(journal); err != nil {
		t.Fatal(err)
	}
	r := Runner{Tmux: ctl, Journal: st}
	preRun, err := ctl.Observe(context.Background(), base.Name)
	if err != nil {
		t.Fatal(err)
	}
	if core.SnapshotFingerprint(*preRun) != plan.StartSnapshotFingerprint {
		beforeJSON, _ := json.Marshal(snapshot)
		afterJSON, _ := json.Marshal(preRun)
		t.Fatalf("snapshot changed before runner: %s\n%s", beforeJSON, afterJSON)
	}
	if err := r.Execute(context.Background(), base.Name, journal.ID, plan); err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if got := isolatedTmuxOut(t, "show-options", "-gqv", "renumber-windows"); got != "on" {
		t.Fatalf("runner failed to restore global renumber-windows: %q", got)
	}
	created, err := ctl.Observe(context.Background(), base.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Target.Windows) != 2 {
		t.Fatalf("windows after execution=%d, want 2", len(created.Target.Windows))
	}
	resolved := core.Resolve(desired, *created)
	if resolved.Status != core.ResolutionReady || len(resolved.Missing) != 0 || len(resolved.Extra) != 0 || len(resolved.Windows) != 2 {
		t.Fatalf("post-execution resolution = %+v", resolved)
	}
	if next := core.Plan(desired, *created, resolved, core.PolicyPreserve); next.Status != core.PlanNoop {
		t.Fatalf("post-execution plan = %+v, want no-op", next)
	}
	if err := r.Execute(context.Background(), base.Name, journal.ID, plan); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	final, err := ctl.Observe(context.Background(), base.Name)
	if err != nil || len(final.Target.Windows) != 2 {
		t.Fatalf("retry duplicated window: snapshot=%+v err=%v", final, err)
	}
}

func TestRunnerExactRebuildRecoversPartialWindowSet(t *testing.T) {
	root := tmuxtest.Isolate(t)
	st, err := store.OpenWithConfig(&config.Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctl, _ := tmux.New()
	isolatedTmuxOut(t, "new-session", "-d", "-s", "runner-hard", "-c", "/tmp", "sleep", "310")
	snapshot, err := ctl.Observe(context.Background(), "runner-hard")
	if err != nil {
		t.Fatal(err)
	}
	ids := []struct {
		kind tmux.IdentityKind
		id   string
		lid  string
	}{
		{tmux.IdentitySession, snapshot.Target.TmuxID, "00000000-0000-4000-8000-000000000031"},
		{tmux.IdentityWindow, snapshot.Target.Windows[0].TmuxID, "00000000-0000-4000-8000-000000000032"},
		{tmux.IdentityPane, snapshot.Target.Windows[0].Panes[0].TmuxID, "00000000-0000-4000-8000-000000000033"},
	}
	for _, item := range ids {
		if err := ctl.SetStableID(context.Background(), item.kind, item.id, item.lid); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = ctl.Observe(context.Background(), "runner-hard")
	if err != nil {
		t.Fatal(err)
	}
	base := snapshot.Target
	base.Windows = append([]model.Window(nil), snapshot.Target.Windows...)
	base.Windows[0].Panes = append([]model.Pane(nil), snapshot.Target.Windows[0].Panes...)
	base.ClassifierVersion = 1
	base.SchemaVersion = model.CurrentBaselineSchemaVersion
	if err := st.SaveBaseline(&base); err != nil {
		t.Fatal(err)
	}
	isolatedTmuxOut(t, "new-window", "-d", "-t", snapshot.TargetSessionID+":7", "-n", "temporary-extra", "-c", "/tmp", "sleep", "311")
	snapshot, err = ctl.Observe(context.Background(), "runner-hard")
	if err != nil {
		t.Fatal(err)
	}
	desired := core.DesiredState{Session: base, ClassifierVersion: 1}
	resolution := core.Resolve(desired, *snapshot)
	plan := core.Plan(desired, *snapshot, resolution, core.PolicyExact)
	if plan.Status != core.PlanReady || len(plan.Operations) != 1 || plan.Operations[0].Kind != core.OperationExactRebuild {
		t.Fatalf("exact plan = %+v; resolution=%+v", plan, resolution)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	journal := model.ReconcileJournal{ID: "run-hard", SessionKey: base.LogicalID, SessionName: base.Name, SessionLogicalID: base.LogicalID, Policy: string(plan.Policy), Status: model.JournalPlanned, PlanJSON: string(body)}
	if err := st.StartReconcile(journal); err != nil {
		t.Fatal(err)
	}
	exact := plan.Operations[0]
	isolatedTmuxOut(t, "set-option", "-t", base.Name, "renumber-windows", "off")
	first := base.Windows[0]
	stageIndex := maxWindowIndex(exact.ExactOriginalWindows)
	if desiredMax := maxWindowIndex(base.Windows); desiredMax > stageIndex {
		stageIndex = desiredMax
	}
	stageIndex++
	if _, err := ctl.CreateWindowWithOperationKeepingMarkers(context.Background(), snapshot.TargetSessionID, stageIndex, stageWindow(exact, first), snapshot.Target.Cwd, exactWindowOperationID(exact, first.Idx)); err != nil {
		t.Fatalf("simulate staged baseline creation before interruption: %v", err)
	}
	for _, original := range exact.ExactOriginalWindows {
		if err := ctl.KillWindowAt(context.Background(), base.Name, original.TmuxID, original.Idx); err != nil {
			t.Fatalf("simulate deletion of original window %s: %v", original.TmuxID, err)
		}
	}
	partial, err := ctl.Observe(context.Background(), base.Name)
	if err != nil {
		t.Fatal(err)
	}
	firstRuntimeID := targetWindowIDByOperation(*partial, exactWindowOperationID(exact, first.Idx))
	firstLiveWindow, firstFound := targetWindowByRuntime(partial.Target, firstRuntimeID)
	windowMarker := objectByRuntime(partial.Objects, model.ObjectWindow, firstRuntimeID)
	if !firstFound || len(firstLiveWindow.Panes) != 1 {
		t.Fatalf("partial exact baseline window was not observable: %+v", partial.Target.Windows)
	}
	paneMarker := objectByRuntime(partial.Objects, model.ObjectPane, firstLiveWindow.Panes[0].TmuxID)
	if windowMarker == nil || windowMarker.OperationID != exactWindowOperationID(exact, first.Idx) || paneMarker == nil || paneMarker.OperationID != exactPaneOperationID(exactWindowOperationID(exact, first.Idx), 0) {
		t.Fatalf("exact create did not retain recovery markers: window=%+v pane=%+v", windowMarker, paneMarker)
	}
	if err := st.UpdateReconcile(journal.ID, model.JournalNeedsAttention, 0, "simulated interruption after partial exact rebuild"); err != nil {
		t.Fatal(err)
	}
	r := Runner{Tmux: ctl, Journal: st}
	if err := ctl.ClearOperationID(context.Background(), tmux.IdentityWindow, firstRuntimeID); err != nil {
		t.Fatal(err)
	}
	if err := ctl.ClearOperationID(context.Background(), tmux.IdentityPane, firstLiveWindow.Panes[0].TmuxID); err != nil {
		t.Fatal(err)
	}
	beforeUnprovenRetry := isolatedTmuxOut(t, "list-windows", "-t", base.Name, "-F", "#{window_id}")
	if err := r.Execute(context.Background(), base.Name, journal.ID, plan); err == nil {
		t.Fatal("exact recovery adopted a copied slot tag without this operation's marker")
	}
	if after := isolatedTmuxOut(t, "list-windows", "-t", base.Name, "-F", "#{window_id}"); after != beforeUnprovenRetry {
		t.Fatalf("unproven exact recovery mutated windows: before=%q after=%q", beforeUnprovenRetry, after)
	}
	windowOperationID := exactWindowOperationID(exact, first.Idx)
	if err := ctl.SetOperationID(context.Background(), tmux.IdentityWindow, firstRuntimeID, windowOperationID); err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetOperationID(context.Background(), tmux.IdentityPane, firstLiveWindow.Panes[0].TmuxID, exactPaneOperationID(windowOperationID, 0)); err != nil {
		t.Fatal(err)
	}
	if err := ctl.UnsetStableID(context.Background(), tmux.IdentityWindow, firstRuntimeID); err != nil {
		t.Fatal(err)
	}
	if err := ctl.UnsetStableID(context.Background(), tmux.IdentityPane, firstLiveWindow.Panes[0].TmuxID); err != nil {
		t.Fatal(err)
	}
	if err := r.Execute(context.Background(), base.Name, journal.ID, plan); err != nil {
		t.Fatalf("execute exact plan: %v", err)
	}
	final, err := ctl.Observe(context.Background(), base.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Target.Windows) != 1 || final.Target.Windows[0].SlotID != base.Windows[0].SlotID || final.Target.Windows[0].Panes[0].SlotID != base.Windows[0].Panes[0].SlotID {
		t.Fatalf("exact rebuild did not restore baseline logical IDs: %+v", final.Target.Windows)
	}
}
