package tmux

import (
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func TestBuildSnapshotDeduplicatesLinkedWindowAndKeepsMemberships(t *testing.T) {
	target := &model.Session{Name: "work", TmuxID: "$1", LogicalID: "session-work", Windows: []model.Window{{Idx: 1, TmuxID: "@4", SlotID: "window-editor", Panes: []model.Pane{{TmuxID: "%8", SlotID: "pane-editor"}}}}}
	got, err := buildSnapshot(target, "server-a",
		"work\t$1\tsession-work\t\nother\t$2\tsession-other\t",
		"$1\t@4\t1\twindow-editor\t\t\t1\n$2\t@4\t3\twindow-editor\t\t\t1",
		"@4\t%8\tpane-editor\t",
	)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if !got.Complete || got.TargetSessionID != "$1" || len(got.Sessions) != 2 || len(got.Objects) != 4 || len(got.Memberships) != 2 {
		t.Fatalf("snapshot cardinality/completeness = %+v", got)
	}
	if got.Memberships[0].SessionID != "$1" || got.Memberships[0].Index != 1 || got.Memberships[1].SessionID != "$2" || got.Memberships[1].Index != 3 {
		t.Fatalf("linked-window memberships = %+v", got.Memberships)
	}
}

func TestBuildSnapshotRejectsInconsistentReferences(t *testing.T) {
	target := &model.Session{Name: "work", TmuxID: "$1"}
	_, err := buildSnapshot(target, "server-a",
		"work\t$1\t\t",
		"$9\t@4\t1\t\t\t\t1",
		"@4\t%8\t\t",
	)
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("unknown membership session error = %v", err)
	}
}

func TestBuildSnapshotRejectsTargetRuntimeIdentityChange(t *testing.T) {
	target := &model.Session{Name: "work", TmuxID: "$1"}
	_, err := buildSnapshot(target, "server-a", "work\t$2\t\t", "$2\t@4\t1\t\t\t\t1", "@4\t%8\t\t")
	if err == nil || !strings.Contains(err.Error(), "target session identity changed") {
		t.Fatalf("target identity error = %v", err)
	}
}

func TestBuildSnapshotRejectsMalformedFormattedRows(t *testing.T) {
	target := &model.Session{Name: "work", TmuxID: "$1"}
	_, err := buildSnapshot(target, "server-a", "work\t$1", "$1\t@4\t1\t\t", "@4\t%8\t\t")
	if err == nil || !strings.Contains(err.Error(), "expected 4") {
		t.Fatalf("malformed session row error = %v", err)
	}
}

func TestBuildSnapshotRejectsTwoWindowsAtSameSessionIndex(t *testing.T) {
	target := &model.Session{Name: "work", TmuxID: "$1", Windows: []model.Window{{Idx: 1, TmuxID: "@1"}}}
	_, err := buildSnapshot(target, "server-a", "work\t$1\t\t", "$1\t@1\t1\t\t\t\t1\n$1\t@2\t1\t\t\t\t1", "@1\t%1\t\t\n@2\t%2\t\t")
	if err == nil || !strings.Contains(err.Error(), "duplicate window index") {
		t.Fatalf("duplicate index error = %v", err)
	}
}
