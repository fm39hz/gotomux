package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

func runIsolatedTmux(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

func TestCtlObserveReadsLogicalOptionsAndServerMembership(t *testing.T) {
	tmuxtest.Isolate(t)
	runIsolatedTmux(t, "new-session", "-d", "-s", "identity", "-c", "/tmp", "sleep", "300")
	sessionRow := runIsolatedTmux(t, "list-sessions", "-F", "#{session_name}\t#{session_id}")
	sessionID := strings.SplitN(sessionRow, "\t", 2)[1]
	windowID := runIsolatedTmux(t, "list-windows", "-t", "=identity", "-F", "#{window_id}")
	paneID := runIsolatedTmux(t, "list-panes", "-t", windowID, "-F", "#{pane_id}")
	ctl, _ := New()
	ctx := context.Background()
	for _, target := range []struct {
		kind IdentityKind
		id   string
		lid  string
	}{
		{IdentitySession, sessionID, "00000000-0000-4000-8000-000000000001"},
		{IdentityWindow, windowID, "00000000-0000-4000-8000-000000000002"},
		{IdentityPane, paneID, "00000000-0000-4000-8000-000000000003"},
	} {
		if err := ctl.SetStableID(ctx, target.kind, target.id, target.lid); err != nil {
			t.Fatalf("set %s logical ID for runtime ID %q (session=%q window=%q pane=%q): %v", target.kind, target.id, sessionID, windowID, paneID, err)
		}
		if got, err := ctl.GetStableID(ctx, target.kind, target.id); err != nil || got != target.lid {
			t.Fatalf("read %s logical ID = %q, %v; want %q", target.kind, got, err, target.lid)
		}
	}
	runIsolatedTmux(t, "new-session", "-d", "-s", "other", "-c", "/tmp", "sleep", "302")
	runIsolatedTmux(t, "link-window", "-s", windowID, "-t", "=other:7")
	for _, target := range []struct {
		kind IdentityKind
		id   string
	}{
		{IdentitySession, sessionID},
		{IdentityWindow, windowID},
		{IdentityPane, paneID},
	} {
		if err := ctl.SetOperationID(ctx, target.kind, target.id, "plan-123/op-0001"); err != nil {
			t.Fatalf("set %s operation marker: %v", target.kind, err)
		}
	}
	snapshot, err := ctl.Observe(ctx, "identity")
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !snapshot.Complete || snapshot.Target.TmuxID != sessionID || snapshot.Target.LogicalID != "00000000-0000-4000-8000-000000000001" || len(snapshot.Target.Windows) != 1 {
		t.Fatalf("target snapshot = %+v", snapshot.Target)
	}
	if snapshot.Target.Windows[0].SlotID != "00000000-0000-4000-8000-000000000002" || snapshot.Target.Windows[0].Panes[0].SlotID != "00000000-0000-4000-8000-000000000003" {
		t.Fatalf("window/pane slot IDs = %+v", snapshot.Target.Windows[0])
	}
	if len(snapshot.Sessions) != 2 || len(snapshot.Objects) != 6 || len(snapshot.Memberships) != 3 {
		t.Fatalf("server-wide object and membership index = %+v", snapshot)
	}
	linkedCount := 0
	for _, membership := range snapshot.Memberships {
		if membership.WindowID == windowID {
			linkedCount++
		}
	}
	if linkedCount != 2 {
		t.Fatalf("linked window membership count = %d, want 2: %+v", linkedCount, snapshot.Memberships)
	}
	var otherSessionID string
	for _, session := range snapshot.Sessions {
		if session.Name == "other" {
			otherSessionID = session.RuntimeID
		}
	}
	if err := ctl.MoveWindowTo(ctx, otherSessionID, windowID, 7, 8); err != nil {
		t.Fatalf("move linked membership by runtime identity: %v", err)
	}
	afterMove, err := ctl.Observe(ctx, "identity")
	if err != nil {
		t.Fatal(err)
	}
	identityMember, otherMember := false, false
	for _, membership := range afterMove.Memberships {
		if membership.WindowID == windowID && membership.SessionID == sessionID && membership.Index == 1 {
			identityMember = true
		}
		if membership.WindowID == windowID && membership.SessionID == otherSessionID && membership.Index == 8 {
			otherMember = true
		}
	}
	if !identityMember || !otherMember {
		t.Fatalf("move changed wrong linked membership: %+v", afterMove.Memberships)
	}
	for _, object := range snapshot.Objects {
		if object.RuntimeID == sessionID || object.RuntimeID == windowID || object.RuntimeID == paneID {
			if object.OperationID != "plan-123/op-0001" {
				t.Errorf("operation marker for %s = %q", object.RuntimeID, object.OperationID)
			}
		}
	}
}

func TestSetStableIDRejectsMalformedIDBeforeCallingTmux(t *testing.T) {
	ctl := &Ctl{}
	err := ctl.SetStableID(context.Background(), IdentityWindow, "@1", "not-a-uuid")
	if err == nil || !strings.Contains(err.Error(), "invalid logical ID") {
		t.Fatalf("malformed logical ID error = %v", err)
	}
}

func TestCreateWindowWithOperationIsIdempotentAndTagsObjects(t *testing.T) {
	tmuxtest.Isolate(t)
	runIsolatedTmux(t, "new-session", "-d", "-s", "create-op", "-c", "/tmp", "sleep", "300")
	ctl, _ := New()
	ctx := context.Background()
	before, err := ctl.Observe(ctx, "create-op")
	if err != nil {
		t.Fatal(err)
	}
	want := model.Window{Idx: 7, Name: "agent", Role: "agent", SlotID: "00000000-0000-4000-8000-000000000011", Cwd: "/tmp", Panes: []model.Pane{{Idx: 0, Cmd: "sleep", StartCmd: "sleep 301", Cwd: "/tmp", SlotID: "00000000-0000-4000-8000-000000000012"}}}
	windowID, err := ctl.CreateWindowWithOperation(ctx, before.TargetSessionID, want.Idx, want, "/tmp", "plan-create/op-0001")
	if err != nil {
		t.Fatalf("create operation window: %v", err)
	}
	after, err := ctl.Observe(ctx, "create-op")
	if err != nil {
		t.Fatal(err)
	}
	window, ok := findDetailedWindow(after, windowID)
	if !ok || window.Idx != want.Idx || window.SlotID != want.SlotID || len(window.Panes) != 1 || window.Panes[0].SlotID != want.Panes[0].SlotID {
		t.Fatalf("created window state = %+v, found=%v", window, ok)
	}
	if len(after.Target.Windows) != len(before.Target.Windows)+1 {
		t.Fatalf("window count after create=%d, before=%d", len(after.Target.Windows), len(before.Target.Windows))
	}
	if _, err := ctl.CreateWindowWithOperation(ctx, before.TargetSessionID, want.Idx, want, "/tmp", "plan-create/op-0001"); err != nil {
		t.Fatalf("resume completed create operation: %v", err)
	}
	afterRetry, err := ctl.Observe(ctx, "create-op")
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRetry.Target.Windows) != len(before.Target.Windows)+1 {
		t.Fatalf("retry duplicated window: count=%d", len(afterRetry.Target.Windows))
	}
}

func TestCreateWindowWithOperationResumesAfterPartialWindowCreation(t *testing.T) {
	tmuxtest.Isolate(t)
	runIsolatedTmux(t, "new-session", "-d", "-s", "create-resume", "-c", "/tmp", "sleep", "300")
	ctl, _ := New()
	ctx := context.Background()
	before, err := ctl.Observe(ctx, "create-resume")
	if err != nil {
		t.Fatal(err)
	}
	want := model.Window{Idx: 7, Name: "agent", Role: "agent", SlotID: "00000000-0000-4000-8000-000000000041", Cwd: "/tmp", Panes: []model.Pane{
		{Idx: 0, Cmd: "sleep", StartCmd: "sleep 301", Cwd: "/tmp", SlotID: "00000000-0000-4000-8000-000000000042"},
		{Idx: 1, Cmd: "sleep", StartCmd: "sleep 302", Cwd: "/tmp", SlotID: "00000000-0000-4000-8000-000000000043"},
	}}
	opID := "plan-create-resume/op-0001"
	markerName := operationWindowName(opID)
	runIsolatedTmux(t, "new-window", "-d", "-t", before.TargetSessionID+":7", "-n", markerName, "-c", "/tmp", "sleep", "301")
	partialSnapshot, err := ctl.Observe(ctx, "create-resume")
	if err != nil {
		t.Fatal(err)
	}
	var partialWindow model.Window
	for _, candidate := range partialSnapshot.Target.Windows {
		if candidate.Name == markerName {
			partialWindow = candidate
			break
		}
	}
	if partialWindow.TmuxID == "" {
		t.Fatalf("operation marker window name %q was not found: %+v", markerName, partialSnapshot.Target.Windows)
	}
	if err := ctl.SetOperationID(ctx, IdentityWindow, partialWindow.TmuxID, opID); err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetOperationID(ctx, IdentityPane, partialWindow.Panes[0].TmuxID, paneOperationID(opID, 0)); err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetStableID(ctx, IdentityWindow, partialWindow.TmuxID, want.SlotID); err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetStableID(ctx, IdentityPane, partialWindow.Panes[0].TmuxID, want.Panes[0].SlotID); err != nil {
		t.Fatal(err)
	}
	windowID, err := ctl.CreateWindowWithOperation(ctx, before.TargetSessionID, want.Idx, want, "/tmp", opID)
	if err != nil {
		t.Fatalf("resume partial window create: %v", err)
	}
	after, err := ctl.Observe(ctx, "create-resume")
	if err != nil {
		t.Fatal(err)
	}
	window, ok := findDetailedWindow(after, windowID)
	if !ok || len(window.Panes) != 2 || window.Panes[0].SlotID != want.Panes[0].SlotID || window.Panes[1].SlotID != want.Panes[1].SlotID {
		t.Fatalf("resumed window = %+v found=%v", window, ok)
	}
	if len(after.Target.Windows) != len(before.Target.Windows)+1 {
		t.Fatalf("partial recovery duplicated window: before=%d after=%d", len(before.Target.Windows), len(after.Target.Windows))
	}
}

func TestAddPaneWithOperationRecoversUntaggedMutation(t *testing.T) {
	tmuxtest.Isolate(t)
	runIsolatedTmux(t, "new-session", "-d", "-s", "pane-recovery", "-c", "/tmp", "sleep", "300")
	ctl, _ := New()
	ctx := context.Background()
	before, err := ctl.Observe(ctx, "pane-recovery")
	if err != nil {
		t.Fatal(err)
	}
	window := before.Target.Windows[0]
	if err := ctl.SetStableID(ctx, IdentityPane, window.Panes[0].TmuxID, "00000000-0000-4000-8000-000000000021"); err != nil {
		t.Fatal(err)
	}
	// Simulate a process interruption after tmux split the pane but before the
	// caller could tag it or advance the operation journal.
	newPaneID := runIsolatedTmux(t, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", window.TmuxID, "-h", "-c", "/tmp", "sleep", "301")
	want := model.Pane{Idx: 1, Cmd: "sleep", StartCmd: "sleep 301", Cwd: "/tmp", SlotID: "00000000-0000-4000-8000-000000000022"}
	got, err := ctl.AddPaneWithOperation(ctx, before.TargetSessionID, window.TmuxID, 1, []string{window.Panes[0].TmuxID}, []string{"00000000-0000-4000-8000-000000000021"}, want, "plan-pane/op-0001")
	if err != nil {
		t.Fatalf("recover untagged pane: %v", err)
	}
	if got != newPaneID {
		t.Fatalf("recovered pane ID=%q, want %q", got, newPaneID)
	}
	if id, err := ctl.GetStableID(ctx, IdentityPane, newPaneID); err != nil || id != want.SlotID {
		t.Fatalf("recovered pane slot ID=%q err=%v", id, err)
	}
	if _, err := ctl.AddPaneWithOperation(ctx, before.TargetSessionID, window.TmuxID, 1, []string{window.Panes[0].TmuxID}, []string{"00000000-0000-4000-8000-000000000021"}, want, "plan-pane/op-0001"); err != nil {
		t.Fatalf("retry recovered pane: %v", err)
	}
	after, err := ctl.Observe(ctx, "pane-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Target.Windows[0].Panes) != 2 {
		t.Fatalf("pane retry duplicated split: %+v", after.Target.Windows[0].Panes)
	}
}
