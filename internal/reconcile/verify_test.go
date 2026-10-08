package reconcile

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func TestVerifyRequiresBaselineWindowsAndPaneCounts(t *testing.T) {
	base := &model.Session{Name: "s", Windows: []model.Window{{Idx: 0, Name: "editor", Panes: []model.Pane{{Cmd: "nvim"}, {}}}}}
	live := &model.Session{Name: "s", Windows: []model.Window{{Idx: 0, Name: "editor", Panes: []model.Pane{{Cmd: "nvim"}}}}}
	if err := Verify(base, live, false); err == nil {
		t.Fatal("soft verification accepted a missing baseline pane")
	}
	if err := Verify(base, live, true); err == nil {
		t.Fatal("hard verification accepted a missing baseline pane")
	}
}

func TestVerifyAllowsPreservedExtraPanesOnlyInSoftMode(t *testing.T) {
	base := &model.Session{Name: "s", Windows: []model.Window{{Idx: 0, Name: "shell", Panes: []model.Pane{{}, {}}}}}
	live := &model.Session{Name: "s", Windows: []model.Window{{Idx: 0, Name: "shell", Panes: []model.Pane{{}, {}, {}}}}}
	if err := Verify(base, live, false); err != nil {
		t.Fatalf("soft verification rejected preserved pane: %v", err)
	}
	if err := Verify(base, live, true); err == nil {
		t.Fatal("hard verification accepted an extra pane")
	}
}
