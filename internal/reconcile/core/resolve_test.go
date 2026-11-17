package reconcile

import (
	"fmt"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func identityFixture(baseWindows []model.Window, liveWindows []model.Window) (model.Session, model.Snapshot) {
	next := 2
	for wi := range baseWindows {
		if baseWindows[wi].SlotID == "" {
			baseWindows[wi].SlotID = fmt.Sprintf("00000000-0000-4000-8000-%012x", next)
			next++
		}
		for pi := range baseWindows[wi].Panes {
			if baseWindows[wi].Panes[pi].SlotID == "" {
				baseWindows[wi].Panes[pi].SlotID = fmt.Sprintf("00000000-0000-4000-8000-%012x", next)
				next++
			}
		}
	}
	base := model.Session{Name: "old-name", LogicalID: "00000000-0000-4000-8000-000000000001", SchemaVersion: model.CurrentBaselineSchemaVersion, TmuxID: "$1", ServerKey: "server-a", Cwd: "/work", Windows: baseWindows}
	live := model.Session{Name: "new-name", LogicalID: base.LogicalID, TmuxID: "$1", ServerKey: "server-a", Cwd: "/work", Windows: liveWindows}
	snapshot := model.Snapshot{ServerKey: "server-a", Complete: true, Target: model.ObserveSession(live), TargetSessionID: "$1", Sessions: []model.SessionIdentity{{Name: live.Name, LogicalID: live.LogicalID, RuntimeID: live.TmuxID}}}
	snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectSession, LogicalID: live.LogicalID, RuntimeID: live.TmuxID})
	for _, w := range liveWindows {
		snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectWindow, SlotID: w.SlotID, RuntimeID: w.TmuxID, Layout: w.Layout, PaneCount: len(w.Panes)})
		snapshot.Memberships = append(snapshot.Memberships, model.WindowMembership{SessionID: live.TmuxID, WindowID: w.TmuxID, Index: w.Idx})
		for _, p := range w.Panes {
			snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectPane, SlotID: p.SlotID, RuntimeID: p.TmuxID, ParentID: w.TmuxID})
		}
	}
	return base, snapshot
}

func TestResolveUsesLogicalIdentityAcrossRenameAndRenumber(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1", Cmd: "nvim"}}}
	liveWindow := baseWindow
	liveWindow.Idx, liveWindow.Name = 8, "codex"
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || got.TargetSession.Evidence[0].Class != EvidenceLogical || len(got.Windows) != 1 || got.Windows[0].Match.LiveRuntimeID != "@1" || len(got.Ambiguous) != 0 {
		t.Fatalf("logical identity resolution = %+v", got)
	}
}

func TestResolveUsesLogicalIdentityAcrossServerGenerationChange(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1", Cmd: "nvim"}}}
	liveWindow := baseWindow
	liveWindow.TmuxID, liveWindow.Panes[0].TmuxID, liveWindow.Idx = "@9", "%9", 9
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	base.ServerKey, base.TmuxID = "server-old", "$1"
	snapshot.ServerKey, snapshot.Target.ServerKey = "server-new", "server-new"
	snapshot.Target.TmuxID, snapshot.TargetSessionID = "$9", "$9"
	snapshot.Target.Windows = []model.Window{liveWindow}
	snapshot.Sessions[0].RuntimeID = "$9"
	snapshot.Objects[0].RuntimeID = "$9"
	snapshot.Objects[1].RuntimeID = "@9"
	snapshot.Objects[2].RuntimeID, snapshot.Objects[2].ParentID = "%9", "@9"
	snapshot.Memberships[0] = model.WindowMembership{SessionID: "$9", WindowID: "@9", Index: 9}
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Windows) != 1 || got.Windows[0].Match.Evidence[0].Class != EvidenceLogical {
		t.Fatalf("cross-generation logical match = %+v", got)
	}
}

func TestResolveRejectsConflictingSessionIdentity(t *testing.T) {
	base, snapshot := identityFixture(nil, nil)
	snapshot.Target.LogicalID = "00000000-0000-4000-8000-000000000099"
	snapshot.Sessions[0].LogicalID = snapshot.Target.LogicalID
	snapshot.Objects[0].LogicalID = snapshot.Target.LogicalID
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionConflict || len(got.Conflicts) == 0 || len(got.Windows) != 0 {
		t.Fatalf("session identity conflict resolution = %+v", got)
	}
}

func TestResolveRejectsBaselineMissingSessionOrSlotIdentity(t *testing.T) {
	window := model.Window{Idx: 1, Name: "editor", Panes: []model.Pane{{Idx: 0, Cmd: "nvim"}}}
	for _, missing := range []string{"session", "window", "pane"} {
		base, snapshot := identityFixture([]model.Window{window}, []model.Window{window})
		switch missing {
		case "session":
			base.LogicalID = ""
		case "window":
			base.Windows[0].SlotID = ""
		case "pane":
			base.Windows[0].Panes[0].SlotID = ""
		}
		got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
		if got.Status != ResolutionConflict || len(got.Conflicts) == 0 {
			t.Errorf("baseline missing %s identity was accepted: %+v", missing, got)
		}
	}
}

func TestResolveTaggedBaselineMatchesSameServerRuntimeIdentity(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, TmuxID: "%1", Cmd: "nvim"}}}
	liveWindow := baseWindow
	liveWindow.Idx, liveWindow.Name = 4, "renamed"
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	snapshot.Target.TmuxID = "$1"
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Windows) != 1 || got.Windows[0].Match.LiveRuntimeID != "@1" || got.Windows[0].Match.Evidence[0].Class != EvidenceRuntime {
		t.Fatalf("same-server runtime resolution = %+v", got)
	}
}

func TestResolveUntaggedReplacementRemainsUnmanagedDespiteMatchingContent(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", TmuxID: "@old", Cwd: "/work", Panes: []model.Pane{{Idx: 0, TmuxID: "%old", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveWindow := model.Window{Idx: 2, Name: "codex", TmuxID: "@9", Cwd: "/work", Panes: []model.Pane{{Idx: 0, TmuxID: "%9", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Windows) != 0 || len(got.Missing) != 1 || len(got.Extra) != 1 {
		t.Fatalf("untagged-live semantic resolution = %+v", got)
	}
}

func TestResolveDoesNotAdoptCodexAsOMPAgent(t *testing.T) {
	baseAgent := model.Window{Idx: 2, Name: "agent", Cwd: "/work", TmuxID: "@old", Panes: []model.Pane{{Idx: 0, Cwd: "/work", Cmd: "omp", CmdPath: "/home/user/.cache/bin/omp", TmuxID: "%old"}}}
	liveCodex := model.Window{Idx: 2, Name: "codex", Cwd: "/work", TmuxID: "@9", Panes: []model.Pane{{Idx: 0, Cwd: "/work", Cmd: "codex", CmdPath: "/usr/bin/codex", TmuxID: "%9"}}}
	base, snapshot := identityFixture([]model.Window{baseAgent}, []model.Window{liveCodex})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Windows) != 0 || len(got.Missing) != 1 || len(got.Extra) != 1 || got.Extra[0] != "@9" {
		t.Fatalf("Codex was misidentified as frozen OMP agent: %+v", got)
	}
}

func TestResolveSameToolRoleDoesNotEstablishWindowOwnership(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "files", Cwd: "/work", TmuxID: "@old", Panes: []model.Pane{{Idx: 0, TmuxID: "%old", Cwd: "/work", Cmd: "yazi", CmdPath: "/usr/bin/yazi"}}}
	liveWindow := model.Window{Idx: 3, Name: "yazi", Cwd: "/work", TmuxID: "@9", Panes: []model.Pane{{Idx: 0, TmuxID: "%9", Cwd: "/work", Cmd: "yazi", CmdPath: "/usr/bin/yazi"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Windows) != 0 || len(got.Missing) != 1 || len(got.Extra) != 1 {
		t.Fatalf("files/yazi role match = %+v", got)
	}
}

func TestResolveDoesNotMatchTaggedReplacementByShape(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@old", Cwd: "/work", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%old", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveWindow := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000099", TmuxID: "@9", Cwd: "/work", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000098", TmuxID: "%9", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Missing) != 1 || len(got.Extra) != 1 || len(got.Windows) != 0 {
		t.Fatalf("different logical identity was reassigned by shape: %+v", got)
	}
}

func TestResolveEquivalentUntaggedWindowsRemainUnmanaged(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveA := model.Window{Idx: 2, Name: "editor-a", Cwd: "/work", TmuxID: "@2", Panes: []model.Pane{{Idx: 0, Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim", TmuxID: "%2"}}}
	liveB := model.Window{Idx: 3, Name: "editor-b", Cwd: "/work", TmuxID: "@3", Panes: []model.Pane{{Idx: 0, Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim", TmuxID: "%3"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveA, liveB})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Ambiguous) != 0 || len(got.Windows) != 0 || len(got.Missing) != 1 || len(got.Extra) != 2 {
		t.Fatalf("equal semantic candidates = %+v", got)
	}
}

func TestResolveNameAloneCannotMatchUntaggedLiveWindow(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "command", Cwd: "/base", Panes: []model.Pane{{Idx: 0, Cwd: "/base"}}}
	liveWindow := model.Window{Idx: 1, Name: "command", Cwd: "/other", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, Cwd: "/other", TmuxID: "%1"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Missing) != 1 || len(got.Extra) != 1 || len(got.Windows) != 0 {
		t.Fatalf("name-only untagged-live candidate was matched: %+v", got)
	}
}

func TestResolveFindsRuntimeWindowMovedOutsideTargetSession(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, TmuxID: "%1"}}}
	current := model.Window{Idx: 1, Name: "shell", TmuxID: "@2", Panes: []model.Pane{{Idx: 0, TmuxID: "%2"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{current})
	snapshot.Sessions = append(snapshot.Sessions, model.SessionIdentity{Name: "other", RuntimeID: "$2"})
	snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectSession, RuntimeID: "$2"}, model.ObjectIdentity{Kind: model.ObjectWindow, RuntimeID: "@1", PaneCount: 1}, model.ObjectIdentity{Kind: model.ObjectPane, RuntimeID: "%1", ParentID: "@1"})
	snapshot.Memberships = append(snapshot.Memberships, model.WindowMembership{SessionID: "$2", WindowID: "@1", Index: 4})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Windows) != 1 || !got.Windows[0].Match.OutsideTarget || got.Windows[0].Match.LiveRuntimeID != "@1" {
		t.Fatalf("moved window identity = %+v", got)
	}
}

func TestResolveMatchingCommandAndScopeDoNotEstablishPaneOwnership(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{{Idx: 0, TmuxID: "%old", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveWindow := model.Window{Idx: 1, Name: "editor", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{{Idx: 0, TmuxID: "%9", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Windows) != 1 || len(got.Windows[0].Panes.Matches) != 0 || len(got.Windows[0].Panes.Missing) != 1 || len(got.Windows[0].Panes.Extra) != 1 {
		t.Fatalf("pane semantic resolution = %+v", got)
	}
}

func TestResolveEquivalentUntaggedPanesRemainUnmanaged(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{{Idx: 0, TmuxID: "%old", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveWindow := model.Window{Idx: 1, Name: "editor", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{
		{Idx: 0, TmuxID: "%9", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"},
		{Idx: 1, TmuxID: "%10", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"},
	}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionReady || len(got.Ambiguous) != 0 || len(got.Windows) != 1 || len(got.Windows[0].Panes.Matches) != 0 || len(got.Windows[0].Panes.Missing) != 1 || len(got.Windows[0].Panes.Extra) != 2 {
		t.Fatalf("ambiguous pane candidates = %+v", got)
	}
}

func TestResolveRejectsLogicalRuntimeWindowConflict(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}}
	liveWindow := baseWindow
	liveWindow.SlotID = "00000000-0000-4000-8000-000000000099"
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	snapshot.Objects[1].SlotID = liveWindow.SlotID
	snapshot.Target.Windows[0].SlotID = liveWindow.SlotID
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionConflict || len(got.Conflicts) == 0 {
		t.Fatalf("logical/runtime conflict = %+v", got)
	}
}

func TestResolveRejectsDuplicateServerLogicalTags(t *testing.T) {
	windowA := model.Window{Idx: 1, Name: "a", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}}
	windowB := model.Window{Idx: 2, Name: "b", SlotID: windowA.SlotID, TmuxID: "@2", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000004", TmuxID: "%2"}}}
	base, snapshot := identityFixture([]model.Window{windowA}, []model.Window{windowA})
	snapshot.Target.Windows = append(snapshot.Target.Windows, windowB)
	snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectWindow, SlotID: windowB.SlotID, RuntimeID: windowB.TmuxID, PaneCount: 1}, model.ObjectIdentity{Kind: model.ObjectPane, SlotID: windowB.Panes[0].SlotID, RuntimeID: windowB.Panes[0].TmuxID, ParentID: windowB.TmuxID})
	snapshot.Memberships = append(snapshot.Memberships, model.WindowMembership{SessionID: "$1", WindowID: "@2", Index: 2})
	got := Resolve(DesiredState{Baseline: model.BaselineFromSession(base)}, snapshot)
	if got.Status != ResolutionConflict || len(got.Conflicts) == 0 {
		t.Fatalf("duplicate live stable IDs = %+v", got)
	}
}

func TestResolveRecreatedLogicalSessionOnSameServer(t *testing.T) {
	base := model.Session{LogicalID: "instance-a", TmuxID: "$8", ServerKey: "server-a"}
	live := model.Snapshot{ServerKey: "server-a", Target: model.ObservedSession{LogicalID: "instance-a", TmuxID: "$9", ServerKey: "server-a"}}
	match, conflict := resolveSession(model.BaselineFromSession(base), live)
	if conflict != nil || match.LiveRuntimeID != "$9" {
		t.Fatalf("recreated logical instance rejected: match=%+v conflict=%+v", match, conflict)
	}
}
