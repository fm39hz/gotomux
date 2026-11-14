package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
)

type fakePort struct {
	snapshot              model.Snapshot
	moveCalls             int
	createCalls           int
	bindCalls             int
	stableCalls           int
	moveErr               error
	wrongMove             bool
	changeServerAfterMove bool
}

func (f *fakePort) Observe(context.Context, string) (*model.Snapshot, error) {
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

func (f *fakePort) StabilizeWindowIndices(context.Context, string, string) (func() error, error) {
	f.stableCalls++
	return func() error { return nil }, nil
}

func (f *fakePort) MoveWindowTo(_ context.Context, _ string, windowID string, from, to int) error {
	f.moveCalls++
	if f.moveErr != nil {
		return f.moveErr
	}
	for i := range f.snapshot.Target.Windows {
		if f.snapshot.Target.Windows[i].TmuxID == windowID {
			if f.snapshot.Target.Windows[i].Idx != from {
				return fmt.Errorf("move from stale index %d", from)
			}
			if f.wrongMove {
				to++
			}
			f.snapshot.Target.Windows[i].Idx = to
			for mi := range f.snapshot.Memberships {
				if f.snapshot.Memberships[mi].SessionID == f.snapshot.TargetSessionID && f.snapshot.Memberships[mi].WindowID == windowID {
					f.snapshot.Memberships[mi].Index = to
				}
			}
			if f.changeServerAfterMove {
				f.snapshot.ServerKey = "server-restarted"
				f.snapshot.Target.ServerKey = "server-restarted"
			}
			return nil
		}
	}
	return errors.New("window missing")
}

func (f *fakePort) LinkWindowTo(context.Context, string, string, int) error { return nil }
func (f *fakePort) CreateWindowWithOperation(context.Context, string, int, model.Window, string, string) (string, error) {
	f.createCalls++
	return "@new", nil
}
func (f *fakePort) AddPaneWithOperation(context.Context, string, string, int, []string, []string, model.Pane, string) (string, error) {
	return "%new", nil
}
func (f *fakePort) ApplyLayoutAt(context.Context, string, string, int, string) error { return nil }
func (f *fakePort) SelectWindowByID(context.Context, string, string) error           { return nil }
func (f *fakePort) KillWindowAt(context.Context, string, string, int) error          { return nil }
func (f *fakePort) ClearOperationID(context.Context, model.ObjectKind, string) error { return nil }

func (f *fakePort) SetStableID(_ context.Context, kind model.ObjectKind, runtimeID, stableID string) error {
	f.bindCalls++
	switch kind {
	case model.ObjectSession:
		if f.snapshot.Target.TmuxID != runtimeID {
			return fmt.Errorf("wrong session identity target")
		}
		f.snapshot.Target.LogicalID = stableID
		for i := range f.snapshot.Sessions {
			if f.snapshot.Sessions[i].RuntimeID == runtimeID {
				f.snapshot.Sessions[i].LogicalID = stableID
			}
		}
	case model.ObjectWindow:
		for wi := range f.snapshot.Target.Windows {
			if f.snapshot.Target.Windows[wi].TmuxID == runtimeID {
				f.snapshot.Target.Windows[wi].SlotID = stableID
			}
		}
	case model.ObjectPane:
		for wi := range f.snapshot.Target.Windows {
			for pi := range f.snapshot.Target.Windows[wi].Panes {
				if f.snapshot.Target.Windows[wi].Panes[pi].TmuxID == runtimeID {
					f.snapshot.Target.Windows[wi].Panes[pi].SlotID = stableID
				}
			}
		}
	default:
		return fmt.Errorf("unsupported identity kind %s", kind)
	}
	for i := range f.snapshot.Objects {
		if f.snapshot.Objects[i].Kind != kind || f.snapshot.Objects[i].RuntimeID != runtimeID {
			continue
		}
		if kind == model.ObjectSession {
			f.snapshot.Objects[i].LogicalID = stableID
		} else {
			f.snapshot.Objects[i].SlotID = stableID
		}
	}
	return nil
}

func (f *fakePort) UnsetStableID(_ context.Context, kind model.ObjectKind, runtimeID string) error {
	for i := range f.snapshot.Objects {
		if f.snapshot.Objects[i].Kind != kind || f.snapshot.Objects[i].RuntimeID != runtimeID {
			continue
		}
		if kind == model.ObjectSession {
			f.snapshot.Objects[i].LogicalID = ""
			f.snapshot.Target.LogicalID = ""
		} else {
			f.snapshot.Objects[i].SlotID = ""
			for wi := range f.snapshot.Target.Windows {
				if kind == model.ObjectWindow && f.snapshot.Target.Windows[wi].TmuxID == runtimeID {
					f.snapshot.Target.Windows[wi].SlotID = ""
				}
				if kind == model.ObjectPane {
					for pi := range f.snapshot.Target.Windows[wi].Panes {
						if f.snapshot.Target.Windows[wi].Panes[pi].TmuxID == runtimeID {
							f.snapshot.Target.Windows[wi].Panes[pi].SlotID = ""
						}
					}
				}
			}
		}
	}
	return nil
}
func (f *fakePort) CreateWindowWithOperationKeepingMarkers(ctx context.Context, session string, index int, window model.Window, cwd, operationID string) (string, error) {
	return f.CreateWindowWithOperation(ctx, session, index, window, cwd, operationID)
}

type fakeJournal struct {
	entry      model.ReconcileJournal
	updates    int
	failUpdate int
}

func (f *fakeJournal) GetReconcile(string) (*model.ReconcileJournal, error) {
	copy := f.entry
	return &copy, nil
}
func (f *fakeJournal) UpdateReconcile(id string, status model.ReconcileJournalStatus, step int, msg string) error {
	f.updates++
	if f.updates == f.failUpdate {
		return errors.New("injected journal failure")
	}
	if id != f.entry.ID {
		return errors.New("unknown journal")
	}
	f.entry.Status, f.entry.CompletedStep, f.entry.LastError = status, step, msg
	return nil
}

func runnerSnapshot() model.Snapshot {
	win := model.Window{Idx: 1, Name: "editor", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}}
	session := model.Session{Name: "work", LogicalID: "00000000-0000-4000-8000-000000000001", SchemaVersion: model.CurrentBaselineSchemaVersion, TmuxID: "$1", ServerKey: "server-a", Windows: []model.Window{win}}
	return model.Snapshot{ServerKey: "server-a", Complete: true, Target: session, TargetSessionID: "$1", Sessions: []model.SessionIdentity{{Name: "work", LogicalID: session.LogicalID, RuntimeID: "$1"}}, Objects: []model.ObjectIdentity{
		{Kind: model.ObjectSession, LogicalID: session.LogicalID, RuntimeID: "$1"},
		{Kind: model.ObjectWindow, SlotID: win.SlotID, RuntimeID: "@1", Layout: win.Layout, PaneCount: len(win.Panes)},
		{Kind: model.ObjectPane, SlotID: win.Panes[0].SlotID, RuntimeID: "%1", ParentID: "@1"},
	}, Memberships: []model.WindowMembership{{SessionID: "$1", WindowID: "@1", Index: 1}}}
}

func TestRunnerBindsResolvedIdentitiesThroughJournaledOperations(t *testing.T) {
	want := runnerSnapshot()
	live := runnerSnapshot()
	live.Target.LogicalID = ""
	live.Sessions[0].LogicalID = ""
	live.Objects[0].LogicalID = ""
	live.Target.Windows[0].SlotID = ""
	live.Target.Windows[0].Panes[0].SlotID = ""
	live.Objects[1].SlotID = ""
	live.Objects[2].SlotID = ""
	desired := core.DesiredState{Session: want.Target}
	resolution := core.Resolve(desired, live)
	if resolution.Status != core.ResolutionReady {
		t.Fatalf("initial identity resolution = %+v", resolution)
	}
	plan := core.Plan(desired, live, resolution, core.PolicyPreserve)
	if plan.Status != core.PlanReady || len(plan.Operations) != 3 {
		t.Fatalf("planned identity bindings = %+v", plan)
	}
	for _, op := range plan.Operations {
		if op.Kind != core.OperationBindIdentity {
			t.Fatalf("identity plan included an unexpected topology operation: %+v", op)
		}
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	journal := &fakeJournal{entry: model.ReconcileJournal{ID: "run-bind", SessionLogicalID: want.Target.LogicalID, Status: model.JournalPlanned, PlanJSON: string(body)}}
	port := &fakePort{snapshot: live}
	if err := (Runner{Tmux: port, Journal: journal}).Execute(context.Background(), "work", journal.entry.ID, plan); err != nil {
		t.Fatalf("execute identity binding plan: %v", err)
	}
	if port.bindCalls != 3 || port.snapshot.Target.LogicalID != want.Target.LogicalID || port.snapshot.Target.Windows[0].SlotID != want.Target.Windows[0].SlotID || port.snapshot.Target.Windows[0].Panes[0].SlotID != want.Target.Windows[0].Panes[0].SlotID {
		t.Fatalf("verified identities after run: binds=%d snapshot=%+v", port.bindCalls, port.snapshot.Target)
	}
	if journal.entry.CompletedStep != 3 {
		t.Fatalf("journal cursor=%d, want all 3 identity operations", journal.entry.CompletedStep)
	}
}

func TestRunnerRejectsChangedEvidenceBeforeBindingSlot(t *testing.T) {
	base := runnerSnapshot()
	for wi := range base.Target.Windows {
		base.Target.Windows[wi].SlotID = ""
		for pi := range base.Target.Windows[wi].Panes {
			base.Target.Windows[wi].Panes[pi].SlotID = ""
		}
	}
	for i := range base.Objects {
		if base.Objects[i].Kind != model.ObjectSession {
			base.Objects[i].SlotID = ""
		}
	}
	desired := core.DesiredState{Session: runnerSnapshot().Target}
	resolution := core.Resolve(desired, base)
	plan := core.Plan(desired, base, resolution, core.PolicyPreserve)
	if plan.Status != core.PlanReady || len(plan.Operations) != 2 {
		t.Fatalf("binding plan = %+v", plan)
	}
	var windowBind core.Operation
	for _, op := range plan.Operations {
		if op.Target.Kind == model.ObjectWindow {
			windowBind = op
		}
	}
	if windowBind.ID == "" || windowBind.ExpectedFingerprint == "" {
		t.Fatalf("window bind lacks an evidence fingerprint: %+v", windowBind)
	}
	changed := base
	changed.Target.Windows = append([]model.Window(nil), base.Target.Windows...)
	changed.Target.Windows[0].Panes = append([]model.Pane(nil), base.Target.Windows[0].Panes...)
	changed.Target.Windows[0].Panes[0].Cmd = "codex"
	if err := checkPreconditions(windowBind, changed); err == nil {
		t.Fatal("window evidence changed after planning, but slot binding was allowed")
	}
}

func movePlan(snapshot model.Snapshot) core.PlanResult {
	resolution := core.Resolution{Status: core.ResolutionReady, ServerKey: snapshot.ServerKey, TargetSession: core.ObjectMatch{
		Kind: model.ObjectSession, BaselineIndex: -1, BaselineIdentityID: snapshot.Target.LogicalID,
		LiveIdentityID: snapshot.Target.LogicalID, BaselineRuntimeID: snapshot.Target.TmuxID, LiveRuntimeID: snapshot.TargetSessionID,
	}}
	return core.PlanResult{ID: "plan-a", Status: core.PlanReady, Policy: core.PolicyPreserve, DesiredSession: snapshot.Target, ClassifierVersion: 1, Resolution: resolution,
		StartSnapshotFingerprint: core.SnapshotFingerprint(snapshot),
		Operations: []core.Operation{{ID: "plan-a/0001", Kind: core.OperationMoveWindow,
			Target:            core.RuntimeRef{ServerGeneration: snapshot.ServerKey, Kind: model.ObjectWindow, ID: "@1"},
			ExpectedServerKey: snapshot.ServerKey, ExpectedSessionID: snapshot.TargetSessionID,
			ExpectedFingerprint: core.WindowFingerprint(snapshot.Target.Windows[0]), FromIndex: 1, ToIndex: 2}},
	}
}

func TestRunnerAppliesIdentityTargetedOperationAndAdvancesJournal(t *testing.T) {
	initial := runnerSnapshot()
	port := &fakePort{snapshot: initial}
	journal := &fakeJournal{entry: model.ReconcileJournal{ID: "run-1", Status: model.JournalPlanned}}
	r := Runner{Tmux: port, Journal: journal}
	if err := r.Execute(context.Background(), "work", "run-1", movePlan(initial)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if port.moveCalls != 1 || port.stableCalls != 1 || journal.entry.CompletedStep != 1 || journal.entry.Status != model.JournalRunning || port.snapshot.Target.Windows[0].Idx != 2 {
		t.Fatalf("execution state: moves=%d stable=%d journal=%+v window=%+v", port.moveCalls, port.stableCalls, journal.entry, port.snapshot.Target.Windows[0])
	}
}

func TestRunnerRecoversWhenMutationSucceededButJournalAdvanceFailed(t *testing.T) {
	initial := runnerSnapshot()
	port := &fakePort{snapshot: initial}
	journal := &fakeJournal{entry: model.ReconcileJournal{ID: "run-2", Status: model.JournalPlanned}, failUpdate: 2}
	r := Runner{Tmux: port, Journal: journal}
	if err := r.Execute(context.Background(), "work", "run-2", movePlan(initial)); err == nil {
		t.Fatal("injected journal failure was ignored")
	}
	if journal.entry.Status != model.JournalNeedsAttention || port.moveCalls != 1 {
		t.Fatalf("failed journal advance state = %+v, moves=%d", journal.entry, port.moveCalls)
	}
	journal.failUpdate = 0
	if err := r.Execute(context.Background(), "work", "run-2", movePlan(initial)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if port.moveCalls != 1 || journal.entry.CompletedStep != 1 {
		t.Fatalf("recovery duplicated mutation: moves=%d journal=%+v", port.moveCalls, journal.entry)
	}
}

func TestRunnerAbortsStalePlanBeforeAnyMutation(t *testing.T) {
	initial := runnerSnapshot()
	changed := runnerSnapshot()
	changed.Target.Windows[0].Cwd = "/changed"
	port := &fakePort{snapshot: changed}
	journal := &fakeJournal{entry: model.ReconcileJournal{ID: "run-3", Status: model.JournalPlanned}}
	r := Runner{Tmux: port, Journal: journal}
	if err := r.Execute(context.Background(), "work", "run-3", movePlan(initial)); err == nil {
		t.Fatal("stale snapshot was accepted")
	}
	if port.moveCalls != 0 || journal.entry.Status != model.JournalAborted {
		t.Fatalf("stale-plan result = moves:%d journal:%+v", port.moveCalls, journal.entry)
	}
}

func TestRunnerRejectsBlockedPlanWithoutTouchingTmux(t *testing.T) {
	port := &fakePort{snapshot: runnerSnapshot()}
	r := Runner{Tmux: port, Journal: &fakeJournal{entry: model.ReconcileJournal{ID: "run-4", Status: model.JournalPlanned}}}
	plan := core.PlanResult{Status: core.PlanBlocked, BlockedReason: "ambiguous"}
	if err := r.Execute(context.Background(), "work", "run-4", plan); err == nil {
		t.Fatal("blocked plan was executed")
	}
	if port.moveCalls != 0 || port.createCalls != 0 {
		t.Fatalf("blocked plan mutated tmux: %+v", port)
	}
}

func TestRunnerMarksTmuxErrorAsNeedsAttention(t *testing.T) {
	initial := runnerSnapshot()
	port := &fakePort{snapshot: initial, moveErr: errors.New("tmux move failed")}
	journal := &fakeJournal{entry: model.ReconcileJournal{ID: "run-tmux-error", Status: model.JournalPlanned}}
	r := Runner{Tmux: port, Journal: journal}
	if err := r.Execute(context.Background(), "work", journal.entry.ID, movePlan(initial)); err == nil {
		t.Fatal("tmux error was ignored")
	}
	if journal.entry.Status != model.JournalNeedsAttention || journal.entry.CompletedStep != 0 {
		t.Fatalf("tmux error journal state = %+v", journal.entry)
	}
}

func TestRunnerMarksPostconditionMismatchAsNeedsAttention(t *testing.T) {
	initial := runnerSnapshot()
	port := &fakePort{snapshot: initial, wrongMove: true}
	journal := &fakeJournal{entry: model.ReconcileJournal{ID: "run-postcondition", Status: model.JournalPlanned}}
	r := Runner{Tmux: port, Journal: journal}
	if err := r.Execute(context.Background(), "work", journal.entry.ID, movePlan(initial)); err == nil {
		t.Fatal("wrong move postcondition was ignored")
	}
	if journal.entry.Status != model.JournalNeedsAttention || journal.entry.CompletedStep != 0 {
		t.Fatalf("postcondition journal state = %+v", journal.entry)
	}
}

func TestRunnerStopsWhenServerGenerationChangesAfterMutation(t *testing.T) {
	initial := runnerSnapshot()
	port := &fakePort{snapshot: initial, changeServerAfterMove: true}
	journal := &fakeJournal{entry: model.ReconcileJournal{ID: "run-generation", Status: model.JournalPlanned}}
	r := Runner{Tmux: port, Journal: journal}
	if err := r.Execute(context.Background(), "work", journal.entry.ID, movePlan(initial)); err == nil {
		t.Fatal("server generation change was ignored")
	}
	if journal.entry.Status != model.JournalNeedsAttention || journal.entry.CompletedStep != 0 {
		t.Fatalf("generation-change journal state = %+v", journal.entry)
	}
}
