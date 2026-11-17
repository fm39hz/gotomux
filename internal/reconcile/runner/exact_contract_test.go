package runner

import (
	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
	"testing"
)

func TestExactReplacementMustHaveEveryOperationOwnedPaneBeforeRemoval(t *testing.T) {
	wanted := model.Window{Idx: 1, SlotID: "window-slot", Panes: []model.Pane{{SlotID: "pane-slot"}}}
	op := core.Operation{ID: "operation"}
	window := model.Window{TmuxID: "@9", SlotID: stagedWindowSlot(op, wanted), Panes: []model.Pane{{TmuxID: "%9", SlotID: stagedPaneSlot(op, wanted, 0)}}}
	snapshot := model.Snapshot{}
	if err := validateExactReplacement(snapshot, op, wanted, window); err == nil {
		t.Fatal("accepted replacement without pane operation ownership")
	}
	snapshot.Objects = []model.ObjectIdentity{{Kind: model.ObjectPane, RuntimeID: "%9", ParentID: "@9", OperationID: exactPaneOperationID(exactWindowOperationID(op, 1), 0)}}
	if err := validateExactReplacement(snapshot, op, wanted, window); err != nil {
		t.Fatal(err)
	}
	window.Panes[0].SlotID = wanted.Panes[0].SlotID // interruption during promotion
	if err := validateExactReplacement(snapshot, op, wanted, window); err != nil {
		t.Fatalf("already promoted pane cannot resume: %v", err)
	}
	snapshot.Objects[0].OperationID = "foreign"
	if err := validateExactReplacement(snapshot, op, wanted, window); err == nil {
		t.Fatal("accepted foreign operation pane")
	}
}
