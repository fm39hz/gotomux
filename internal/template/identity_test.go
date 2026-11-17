package template

import (
	"context"
	"errors"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type fakeIdentityOps struct {
	snapshot model.Snapshot
	sets     int
	failAt   int
}

func (f *fakeIdentityOps) Observe(_ context.Context, _ string) (*model.Snapshot, error) {
	copy := f.snapshot
	copy.Sessions = append([]model.SessionIdentity(nil), f.snapshot.Sessions...)
	copy.Objects = append([]model.ObjectIdentity(nil), f.snapshot.Objects...)
	copy.Memberships = append([]model.WindowMembership(nil), f.snapshot.Memberships...)
	copy.Target.Windows = append([]model.Window(nil), f.snapshot.Target.Windows...)
	for i := range copy.Target.Windows {
		copy.Target.Windows[i].Panes = append([]model.Pane(nil), f.snapshot.Target.Windows[i].Panes...)
	}
	return &copy, nil
}

func (f *fakeIdentityOps) SetStableID(_ context.Context, kind tmux.IdentityKind, runtimeID, logicalID string) error {
	f.sets++
	if f.failAt == f.sets {
		return errors.New("injected tag failure")
	}
	switch kind {
	case tmux.IdentitySession:
		if f.snapshot.Target.TmuxID != runtimeID {
			return errors.New("wrong session runtime ID")
		}
		f.snapshot.Target.LogicalID = logicalID
		f.snapshot.Sessions[0].LogicalID = logicalID
	case tmux.IdentityWindow:
		for i := range f.snapshot.Target.Windows {
			if f.snapshot.Target.Windows[i].TmuxID == runtimeID {
				f.snapshot.Target.Windows[i].SlotID = logicalID
			}
		}
	case tmux.IdentityPane:
		for wi := range f.snapshot.Target.Windows {
			for pi := range f.snapshot.Target.Windows[wi].Panes {
				if f.snapshot.Target.Windows[wi].Panes[pi].TmuxID == runtimeID {
					f.snapshot.Target.Windows[wi].Panes[pi].SlotID = logicalID
				}
			}
		}
	default:
		return errors.New("unknown identity kind")
	}
	for i := range f.snapshot.Objects {
		if f.snapshot.Objects[i].RuntimeID == runtimeID {
			if kind == tmux.IdentitySession {
				f.snapshot.Objects[i].LogicalID = logicalID
			} else {
				f.snapshot.Objects[i].SlotID = logicalID
			}
		}
	}
	return nil
}

func identitySnapshot() model.Snapshot {
	return model.Snapshot{
		ServerKey: "server-1", Complete: true, TargetSessionID: "$1",
		Target:      model.ObservedSession{Name: "work", TmuxID: "$1", ServerKey: "server-1", Windows: []model.Window{{Idx: 1, TmuxID: "@1", Panes: []model.Pane{{Idx: 0, TmuxID: "%1"}}}}},
		Sessions:    []model.SessionIdentity{{Name: "work", RuntimeID: "$1"}},
		Objects:     []model.ObjectIdentity{{Kind: "session", RuntimeID: "$1"}, {Kind: "window", RuntimeID: "@1"}, {Kind: "pane", RuntimeID: "%1", ParentID: "@1"}},
		Memberships: []model.WindowMembership{{SessionID: "$1", WindowID: "@1", Index: 1}},
	}
}

func TestEnsureLiveIdentitiesAssignsMissingIDsAndIsIdempotent(t *testing.T) {
	ops := &fakeIdentityOps{snapshot: identitySnapshot()}
	got, err := EnsureLiveIdentities(context.Background(), ops, "work")
	if err != nil {
		t.Fatalf("ensure IDs: %v", err)
	}
	if got.LogicalID == "" || got.Windows[0].SlotID == "" || got.Windows[0].Panes[0].SlotID == "" || ops.sets != 3 {
		t.Fatalf("assigned IDs/call count = %+v, %d", got, ops.sets)
	}
	if _, err := EnsureLiveIdentities(context.Background(), ops, "work"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if ops.sets != 3 {
		t.Fatalf("second ensure rewrote existing IDs: set calls=%d", ops.sets)
	}
}

func TestEnsureLiveIdentitiesRejectsDuplicateOrMalformedExistingIDs(t *testing.T) {
	for name, duplicate := range map[string]bool{"duplicate": true, "malformed": false} {
		snapshot := identitySnapshot()
		if duplicate {
			snapshot.Objects[0].LogicalID = "00000000-0000-4000-8000-000000000001"
			snapshot.Objects[1].SlotID = snapshot.Objects[0].LogicalID
		} else {
			snapshot.Objects[0].LogicalID = "not-a-uuid"
		}
		ops := &fakeIdentityOps{snapshot: snapshot}
		if _, err := EnsureLiveIdentities(context.Background(), ops, "work"); err == nil {
			t.Errorf("%s identity state was accepted", name)
		}
		if ops.sets != 0 {
			t.Errorf("%s identity state mutated before validation", name)
		}
	}
}

func TestEnsureLiveIdentitiesCanResumeAfterPartialTagFailure(t *testing.T) {
	ops := &fakeIdentityOps{snapshot: identitySnapshot(), failAt: 2}
	if _, err := EnsureLiveIdentities(context.Background(), ops, "work"); err == nil {
		t.Fatal("injected tag failure was ignored")
	}
	ops.failAt = 0
	if _, err := EnsureLiveIdentities(context.Background(), ops, "work"); err != nil {
		t.Fatalf("resume after partial tagging: %v", err)
	}
	if ops.snapshot.Target.LogicalID == "" || ops.snapshot.Target.Windows[0].SlotID == "" || ops.snapshot.Target.Windows[0].Panes[0].SlotID == "" {
		t.Fatalf("resume left an untagged target: %+v", ops.snapshot.Target)
	}
}

func TestLoadedReceiptMismatchNeverAssignsIdentity(t *testing.T) {
	ops := &fakeIdentityOps{snapshot: identitySnapshot()}
	preferred := ops.snapshot.Target.Session()
	preferred.TmuxID = "$9"
	if _, err := EnsureLiveIdentitiesFromPreset(context.Background(), ops, "work", &preferred); err == nil {
		t.Fatal("accepted another session's creation receipt")
	}
	if ops.sets != 0 {
		t.Fatal("identity was written before validating creation ownership")
	}
}
