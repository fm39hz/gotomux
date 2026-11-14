package reconcile

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func khoCongPolicyFixture() (DesiredState, model.Snapshot) {
	base := model.Session{
		Name: "kho-cong", LogicalID: "00000000-0000-4000-8000-000000000001", SchemaVersion: model.CurrentBaselineSchemaVersion, TmuxID: "$1", ServerKey: "server-a", Cwd: "/work", ClassifierVersion: 1,
		Windows: []model.Window{
			{Idx: 1, Name: "editor", Role: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cmd: "nvim", CmdPath: "/usr/bin/nvim", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}},
			{Idx: 2, Name: "agent", Role: "agent", SlotID: "00000000-0000-4000-8000-000000000004", TmuxID: "@2", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cmd: "omp", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000005", TmuxID: "%2"}}},
		},
	}
	live := model.Session{
		Name: "kho-cong", LogicalID: base.LogicalID, TmuxID: "$1", ServerKey: base.ServerKey, Cwd: base.Cwd, ClassifierVersion: 1,
		Windows: []model.Window{
			base.Windows[0],
			{Idx: 2, Name: "codex", Role: "agent", SlotID: "00000000-0000-4000-8000-000000000006", TmuxID: "@3", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cmd: "codex", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000007", TmuxID: "%3"}}},
		},
	}
	snapshot := model.Snapshot{ServerKey: live.ServerKey, Complete: true, Target: live, TargetSessionID: live.TmuxID, ActiveWindowID: "@3", Sessions: []model.SessionIdentity{{Name: live.Name, LogicalID: live.LogicalID, RuntimeID: live.TmuxID}}}
	snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectSession, LogicalID: live.LogicalID, RuntimeID: live.TmuxID})
	for _, window := range live.Windows {
		snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectWindow, SlotID: window.SlotID, RuntimeID: window.TmuxID, Layout: window.Layout, PaneCount: len(window.Panes)})
		snapshot.Memberships = append(snapshot.Memberships, model.WindowMembership{SessionID: live.TmuxID, WindowID: window.TmuxID, Index: window.Idx})
		for _, pane := range window.Panes {
			snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectPane, SlotID: pane.SlotID, RuntimeID: pane.TmuxID, ParentID: window.TmuxID})
		}
	}
	return DesiredState{Session: base, ClassifierVersion: 1}, snapshot
}

func TestPlanPreserveCreatesMissingBaselineAndKeepsCodexExtra(t *testing.T) {
	desired, live := khoCongPolicyFixture()
	resolution := Resolve(desired, live)
	plan := Plan(desired, live, resolution, PolicyPreserve)
	if plan.Status != PlanReady || plan.Policy != PolicyPreserve || len(plan.Operations) == 0 {
		t.Fatalf("preserve plan = %+v", plan)
	}
	createdAgent := false
	stagedCodex := false
	deleted := false
	activePreserved := false
	for _, op := range plan.Operations {
		if op.Kind == OperationCreateWindow && op.Window.SlotID == desired.Session.Windows[1].SlotID {
			createdAgent = true
		}
		if op.Kind == OperationMoveWindow && op.Target.ID == "@3" && op.Temporary {
			stagedCodex = true
		}
		if op.Kind == OperationExactRebuild {
			deleted = true
		}
		if op.Kind == OperationSelectWindow && op.Target.ID == "@3" && op.Index == 3 {
			activePreserved = true
		}
	}
	if !createdAgent || !stagedCodex || deleted || !activePreserved || len(plan.PreservedWindows) != 1 || plan.PreservedWindows[0] != "@3" {
		t.Fatalf("preserve operations/policy = %+v", plan)
	}
}

func TestPlanDoesNotRenameSemanticAlias(t *testing.T) {
	desired, live := khoCongPolicyFixture()
	baseFiles := model.Window{Idx: 3, Name: "files", Role: "files", SlotID: "00000000-0000-4000-8000-000000000008", TmuxID: "@8", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cmd: "yazi", CmdPath: "/usr/bin/yazi", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000009", TmuxID: "%8"}}}
	liveFiles := baseFiles
	liveFiles.Name, liveFiles.Idx = "yazi", 3
	desired.Session.Windows = []model.Window{baseFiles}
	live.Target.Windows = []model.Window{liveFiles}
	live.ActiveWindowID = ""
	live.Objects = []model.ObjectIdentity{{Kind: model.ObjectSession, LogicalID: live.Target.LogicalID, RuntimeID: "$1"}, {Kind: model.ObjectWindow, SlotID: liveFiles.SlotID, RuntimeID: "@8", PaneCount: 1}, {Kind: model.ObjectPane, SlotID: liveFiles.Panes[0].SlotID, RuntimeID: "%8", ParentID: "@8"}}
	live.Memberships = []model.WindowMembership{{SessionID: "$1", WindowID: "@8", Index: 3}}
	resolution := Resolve(desired, live)
	plan := Plan(desired, live, resolution, PolicyPreserve)
	if plan.Status != PlanNoop || len(plan.Operations) != 0 {
		t.Fatalf("semantic alias should be a no-op: resolution=%+v plan=%+v", resolution, plan)
	}
}

func TestPlanExactIsNoopWhenAlignedAndRebuildsOnlyWhenNeeded(t *testing.T) {
	desired, live := khoCongPolicyFixture()
	// Make the live tree exactly match the baseline, including the runtime
	// identity of the agent window, without changing the baseline definition.
	live.Target.Windows = append([]model.Window(nil), desired.Session.Windows...)
	live.ActiveWindowID = ""
	for i := range live.Target.Windows {
		live.Target.Windows[i].TmuxID = desired.Session.Windows[i].TmuxID
		live.Target.Windows[i].Panes = append([]model.Pane(nil), desired.Session.Windows[i].Panes...)
	}
	live.Objects = []model.ObjectIdentity{{Kind: model.ObjectSession, LogicalID: live.Target.LogicalID, RuntimeID: "$1"}}
	live.Memberships = nil
	for _, window := range live.Target.Windows {
		live.Objects = append(live.Objects, model.ObjectIdentity{Kind: model.ObjectWindow, SlotID: window.SlotID, RuntimeID: window.TmuxID, Layout: window.Layout, PaneCount: len(window.Panes)})
		live.Memberships = append(live.Memberships, model.WindowMembership{SessionID: "$1", WindowID: window.TmuxID, Index: window.Idx})
		for _, pane := range window.Panes {
			live.Objects = append(live.Objects, model.ObjectIdentity{Kind: model.ObjectPane, SlotID: pane.SlotID, RuntimeID: pane.TmuxID, ParentID: window.TmuxID})
		}
	}
	resolution := Resolve(desired, live)
	plan := Plan(desired, live, resolution, PolicyExact)
	if plan.Status != PlanNoop || len(plan.Operations) != 0 {
		t.Fatalf("aligned exact plan = %+v", plan)
	}
	changedDesired, changedLive := khoCongPolicyFixture()
	changedResolution := Resolve(changedDesired, changedLive)
	changed := Plan(changedDesired, changedLive, changedResolution, PolicyExact)
	if changed.Status != PlanReady || len(changed.Operations) != 1 || changed.Operations[0].Kind != OperationExactRebuild || !changed.Operations[0].Destructive {
		t.Fatalf("exact plan should emit one explicit destructive operation: %+v", changed)
	}
}

func TestPlanBlocksAllMutationOnAmbiguityOrConflict(t *testing.T) {
	desired, live := khoCongPolicyFixture()
	for _, status := range []ResolutionStatus{ResolutionAmbiguous, ResolutionConflict} {
		resolution := Resolution{Status: status, ServerKey: live.ServerKey}
		plan := Plan(desired, live, resolution, PolicyExact)
		if plan.Status != PlanBlocked || len(plan.Operations) != 0 {
			t.Errorf("%s resolution produced operations: %+v", status, plan)
		}
	}
}

func TestPlanLinksExistingWindowBackFromAnotherSessionByIdentity(t *testing.T) {
	baselineWindow := model.Window{Idx: 1, Name: "editor", Role: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cmd: "nvim", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}}
	desiredSession := model.Session{Name: "target", LogicalID: "00000000-0000-4000-8000-000000000001", SchemaVersion: model.CurrentBaselineSchemaVersion, TmuxID: "$1", ServerKey: "server-a", Cwd: "/work", ClassifierVersion: 1, Windows: []model.Window{baselineWindow}}
	liveWindow := model.Window{Idx: 2, Name: "shell", TmuxID: "@2", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cwd: "/work", TmuxID: "%2"}}}
	liveSession := model.Session{Name: "target", LogicalID: desiredSession.LogicalID, TmuxID: "$1", ServerKey: desiredSession.ServerKey, Cwd: "/work", Windows: []model.Window{liveWindow}}
	snapshot := model.Snapshot{ServerKey: "server-a", Complete: true, Target: liveSession, TargetSessionID: "$1",
		Sessions: []model.SessionIdentity{{Name: "target", LogicalID: desiredSession.LogicalID, RuntimeID: "$1"}, {Name: "other", RuntimeID: "$2"}},
		Objects: []model.ObjectIdentity{
			{Kind: model.ObjectSession, LogicalID: desiredSession.LogicalID, RuntimeID: "$1"}, {Kind: model.ObjectSession, RuntimeID: "$2"},
			{Kind: model.ObjectWindow, SlotID: baselineWindow.SlotID, RuntimeID: "@1", PaneCount: 1}, {Kind: model.ObjectPane, SlotID: baselineWindow.Panes[0].SlotID, RuntimeID: "%1", ParentID: "@1"},
			{Kind: model.ObjectWindow, RuntimeID: "@2", PaneCount: 1}, {Kind: model.ObjectPane, RuntimeID: "%2", ParentID: "@2"},
		},
		Memberships: []model.WindowMembership{{SessionID: "$2", WindowID: "@1", Index: 4}, {SessionID: "$1", WindowID: "@2", Index: 2}},
	}
	desired := DesiredState{Session: desiredSession, ClassifierVersion: 1}
	resolution := Resolve(desired, snapshot)
	if resolution.Status != ResolutionReady || len(resolution.Windows) != 1 || !resolution.Windows[0].Match.OutsideTarget {
		t.Fatalf("linked window resolution = %+v", resolution)
	}
	plan := Plan(desired, snapshot, resolution, PolicyPreserve)
	if plan.Status != PlanReady || len(plan.Operations) != 1 || plan.Operations[0].Kind != OperationLinkWindow || plan.Operations[0].Target.ID != "@1" || plan.Operations[0].ToIndex != 1 {
		t.Fatalf("linked window plan = %+v", plan)
	}
	exact := Plan(desired, snapshot, resolution, PolicyExact)
	if exact.Status != PlanBlocked || len(exact.Operations) != 0 {
		t.Fatalf("exact policy must not kill a window linked to another session: %+v", exact)
	}
	desired.Session.Windows[0].Panes = append(desired.Session.Windows[0].Panes, model.Pane{Idx: 1, SlotID: "00000000-0000-4000-8000-000000000004", Cwd: "/work"})
	needsRepair := Resolve(desired, snapshot)
	blocked := Plan(desired, snapshot, needsRepair, PolicyPreserve)
	if blocked.Status != PlanBlocked || len(blocked.Operations) != 0 {
		t.Fatalf("preserve must not mutate a shared window's pane tree: %+v", blocked)
	}
}

func TestResolveRejectsUnsupportedBaselineSchema(t *testing.T) {
	base, live := identityFixture([]model.Window{{Idx: 0, Panes: []model.Pane{{Idx: 0}}}}, []model.Window{{Idx: 0, TmuxID: "@1", Panes: []model.Pane{{Idx: 0, TmuxID: "%1"}}}})
	base.SchemaVersion = model.CurrentBaselineSchemaVersion - 1
	got := Resolve(DesiredState{Session: base}, live)
	if got.Status != ResolutionConflict {
		t.Fatalf("unsupported baseline schema resolution = %+v", got)
	}
}

func TestPlanIsDeterministicAndEveryMutationHasIdentityPreconditions(t *testing.T) {
	desired, live := khoCongPolicyFixture()
	resolution := Resolve(desired, live)
	a := Plan(desired, live, resolution, PolicyPreserve)
	b := Plan(desired, live, resolution, PolicyPreserve)
	if a.ID == "" || a.ID != b.ID || len(a.Operations) != len(b.Operations) {
		t.Fatalf("plan IDs/lengths differ: a=%+v b=%+v", a, b)
	}
	for i, op := range a.Operations {
		if op.ID == "" || op.ID != b.Operations[i].ID || op.ExpectedServerKey != live.ServerKey || op.ExpectedSessionID != live.TargetSessionID {
			t.Errorf("operation lacks deterministic identity preconditions: %+v", op)
		}
		if op.Target.ID == "" {
			t.Errorf("mutation targets no tmux object ID: %+v", op)
		}
	}
}

func TestSnapshotFingerprintDoesNotMutateIdentityEvidence(t *testing.T) {
	_, live := khoCongPolicyFixture()
	beforeTarget := live.Target.LogicalID
	beforeSession := live.Sessions[0].LogicalID
	beforeObject := live.Objects[0].LogicalID
	_ = SnapshotFingerprint(live)
	if live.Target.LogicalID != beforeTarget || live.Sessions[0].LogicalID != beforeSession || live.Objects[0].LogicalID != beforeObject {
		t.Fatalf("fingerprint mutated live identity evidence: target=%q session=%q object=%q", live.Target.LogicalID, live.Sessions[0].LogicalID, live.Objects[0].LogicalID)
	}
}

func TestPlanBindsResolvedSessionAndSlotsThroughVerifiedOperations(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveWindow := model.Window{Idx: 1, Name: "renamed", Cwd: "/work", TmuxID: "@9", Panes: []model.Pane{{Idx: 0, Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim", TmuxID: "%9"}}}
	base, live := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	live.Target.LogicalID = ""
	live.Sessions[0].LogicalID = ""
	live.Objects[0].LogicalID = ""
	desired := DesiredState{Session: base, ClassifierVersion: 1}
	resolution := Resolve(desired, live)
	plan := Plan(desired, live, resolution, PolicyPreserve)
	if plan.Status != PlanReady || len(plan.Operations) != 3 {
		t.Fatalf("identity binding plan = %+v; resolution=%+v", plan, resolution)
	}
	for i, kind := range []ObjectKind{ObjectSession, ObjectWindow, ObjectPane} {
		op := plan.Operations[i]
		if op.Kind != OperationBindIdentity || op.Target.Kind != kind || op.Destructive || op.Target.ID == "" || op.TargetIdentityID == "" {
			t.Fatalf("binding operation %d = %+v, want non-destructive %s identity bind", i, op, kind)
		}
		if kind != ObjectSession && op.ExpectedFingerprint == "" {
			t.Errorf("binding operation %d has no precondition fingerprint: %+v", i, op)
		}
	}
}

func TestPreconditionFingerprintsIgnoreTransientExecutablePath(t *testing.T) {
	_, snapshot := khoCongPolicyFixture()
	changed := snapshot
	changed.Target.Windows = append([]model.Window(nil), snapshot.Target.Windows...)
	changed.Target.Windows[0].Panes = append([]model.Pane(nil), snapshot.Target.Windows[0].Panes...)
	changed.Target.Windows[0].Panes[0].CmdPath = ""
	if WindowFingerprint(snapshot.Target.Windows[0]) != WindowFingerprint(changed.Target.Windows[0]) {
		t.Fatal("window precondition changed only because best-effort CmdPath evidence disappeared")
	}
	if SnapshotFingerprint(snapshot) != SnapshotFingerprint(changed) {
		t.Fatal("snapshot precondition changed only because best-effort CmdPath evidence disappeared")
	}
}
