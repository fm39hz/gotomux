package reconcile

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func TestVerifyUsesIdentityAndRoleWhilePreservingExtras(t *testing.T) {
	baseWindow := model.Window{Idx: 3, Name: "files", Role: "files", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cmd: "yazi", CmdPath: "/usr/bin/yazi", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}}
	liveFiles := baseWindow
	liveFiles.Name, liveFiles.TmuxID, liveFiles.Panes[0].TmuxID = "yazi", "@9", "%9"
	extra := model.Window{Idx: 7, Name: "codex", Role: "agent", SlotID: "00000000-0000-4000-8000-000000000004", TmuxID: "@10", Cwd: "/work", Panes: []model.Pane{{Idx: 0, Cmd: "codex", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000005", TmuxID: "%10"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveFiles, extra})
	base.ClassifierVersion = 1
	resolution := Resolve(DesiredState{Session: base, ClassifierVersion: 1}, snapshot)
	if err := Verify(DesiredState{Session: base, ClassifierVersion: 1}, snapshot, resolution, PolicyPreserve, PlanResult{}); err != nil {
		t.Fatalf("preserve verification rejected role alias/extra: %v (resolution=%+v)", err, resolution)
	}
	if err := Verify(DesiredState{Session: base, ClassifierVersion: 1}, snapshot, resolution, PolicyExact, PlanResult{}); err == nil {
		t.Fatal("exact verification accepted preserved extra window")
	}
}

func TestRefreshBindingsFollowsResolutionInsteadOfBaselineIndex(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@old", Cwd: "/work", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%old", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveWindow := baseWindow
	liveWindow.Idx, liveWindow.TmuxID, liveWindow.Panes[0].TmuxID = 4, "@9", "%9"
	extra := model.Window{Idx: 1, Name: "extra", SlotID: "00000000-0000-4000-8000-000000000004", TmuxID: "@8", Cwd: "/work", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000005", TmuxID: "%8", Cwd: "/work"}}}
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow, extra})
	resolution := Resolve(DesiredState{Session: base}, snapshot)
	if resolution.Status != ResolutionReady || len(resolution.Windows) != 1 || resolution.Windows[0].Match.LiveRuntimeID != "@9" {
		t.Fatalf("resolution = %+v", resolution)
	}
	updated, err := RefreshBindings(base, snapshot, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Windows[0].TmuxID != "@9" || updated.Windows[0].Panes[0].TmuxID != "%9" || updated.Windows[0].TmuxID == extra.TmuxID {
		t.Fatalf("runtime bindings were selected by index instead of resolution: %+v", updated.Windows[0])
	}
}

func TestVerifyRejectsMissingBaselineLogicalObjectEvenAtCorrectIndex(t *testing.T) {
	baseWindow := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Cwd: "/work", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1", Cwd: "/work", Cmd: "nvim", CmdPath: "/usr/bin/nvim"}}}
	liveWindow := baseWindow
	liveWindow.SlotID = "00000000-0000-4000-8000-000000000099"
	base, snapshot := identityFixture([]model.Window{baseWindow}, []model.Window{liveWindow})
	snapshot.Objects[1].SlotID = liveWindow.SlotID
	snapshot.Target.Windows[0].SlotID = liveWindow.SlotID
	resolution := Resolve(DesiredState{Session: base}, snapshot)
	if err := Verify(DesiredState{Session: base}, snapshot, resolution, PolicyPreserve, PlanResult{}); err == nil {
		t.Fatal("verify accepted a different window slot at the baseline index")
	}
}
