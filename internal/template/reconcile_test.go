package template

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
	"github.com/fm39hz/gotomux/internal/reconcile/runner"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type noBaselineOps struct{ runner.TmuxPort }

func (noBaselineOps) Observe(_ context.Context, name string) (*model.Snapshot, error) {
	session := model.Session{Name: name, TmuxID: "$1", ServerKey: "test-server"}
	return &model.Snapshot{ServerKey: session.ServerKey, Complete: true, Target: model.ObserveSession(session), TargetSessionID: session.TmuxID, Sessions: []model.SessionIdentity{{Name: name, RuntimeID: session.TmuxID}}, Objects: []model.ObjectIdentity{{Kind: model.ObjectSession, RuntimeID: session.TmuxID}}}, nil
}
func (noBaselineOps) SetStableID(context.Context, tmux.IdentityKind, string, string) error {
	return nil
}

type identifiedTestOps struct {
	runner.TmuxPort
	snapshot model.Snapshot
	sets     int
}

func (o *identifiedTestOps) Observe(context.Context, string) (*model.Snapshot, error) {
	copy := o.snapshot
	return &copy, nil
}

func (o *identifiedTestOps) SetStableID(context.Context, tmux.IdentityKind, string, string) error {
	o.sets++
	return nil
}

type baselineTestStore struct {
	store.Storer
	baseline *model.Session
	started  bool
	active   *model.ReconcileJournal
	journals map[string]model.ReconcileJournal
}

func (s *baselineTestStore) GetBaselineByLogicalID(string) (*model.Session, error) {
	return s.baseline, nil
}

func (s *baselineTestStore) GetActiveReconcile(key string) (*model.ReconcileJournal, error) {
	if s.active == nil || s.active.SessionKey != key || s.active.Status == model.JournalAborted || s.active.Status == model.JournalComplete {
		return nil, nil
	}
	copy := *s.active
	return &copy, nil
}

func (s *baselineTestStore) ListActiveReconciles() ([]model.ReconcileJournal, error) {
	if s.active == nil || s.active.Status == model.JournalAborted || s.active.Status == model.JournalComplete {
		return nil, nil
	}
	return []model.ReconcileJournal{*s.active}, nil
}

func (s *baselineTestStore) StartReconcile(journal model.ReconcileJournal) error {
	s.started = true
	if s.journals == nil {
		s.journals = make(map[string]model.ReconcileJournal)
	}
	s.journals[journal.ID] = journal
	s.active = &journal
	return nil
}

func (s *baselineTestStore) GetReconcile(id string) (*model.ReconcileJournal, error) {
	journal, ok := s.journals[id]
	if !ok {
		return nil, nil
	}
	copy := journal
	return &copy, nil
}

func (s *baselineTestStore) UpdateReconcile(id string, status model.ReconcileJournalStatus, step int, lastError string) error {
	journal, ok := s.journals[id]
	if !ok && s.active != nil && s.active.ID == id {
		journal, ok = *s.active, true
	}
	if !ok {
		return fmt.Errorf("unknown journal %q", id)
	}
	journal.Status, journal.CompletedStep, journal.LastError = status, step, lastError
	if s.journals == nil {
		s.journals = make(map[string]model.ReconcileJournal)
	}
	s.journals[id] = journal
	s.active = &journal
	return nil
}

func (s *baselineTestStore) CommitReconcile(session *model.Session, journalID string) error {
	if session == nil {
		return fmt.Errorf("nil baseline")
	}
	journal, ok := s.journals[journalID]
	if !ok {
		return fmt.Errorf("unknown journal %q", journalID)
	}
	var plan core.PlanResult
	if err := json.Unmarshal([]byte(journal.PlanJSON), &plan); err != nil {
		return err
	}
	if err := s.UpdateReconcile(journalID, model.JournalComplete, len(plan.Operations), ""); err != nil {
		return err
	}
	s.baseline = session
	return nil
}

func TestReconcileRequiresAnExplicitBaseline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	st, err := store.OpenWithConfig(&config.Config{DataDir: filepath.Join(dir, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = ReconcileSession(context.Background(), noBaselineOps{}, st, "unfrozen", false)
	if err == nil || !strings.Contains(err.Error(), "freeze the session first") {
		t.Fatalf("reconcile without baseline error = %v", err)
	}
	base, err := st.GetBaseline("unfrozen")
	if err != nil || base != nil {
		t.Fatalf("missing baseline was accidentally adopted: %+v err=%v", base, err)
	}
}

func TestReconcileRejectsUnidentifiedBaselineBeforeMutation(t *testing.T) {
	baseline := &model.Session{
		Name: "work", LogicalID: "00000000-0000-4000-8000-000000000001", TmuxID: "$1", ServerKey: "server-a",
		Windows: []model.Window{{Idx: 1, Name: "editor", TmuxID: "@1", Panes: []model.Pane{{Idx: 0, TmuxID: "%1", Cmd: "nvim"}}}},
	}
	live := model.Session{Name: "work", LogicalID: baseline.LogicalID, TmuxID: "$1", ServerKey: "server-a", Windows: baseline.Windows}
	ops := &identifiedTestOps{snapshot: model.Snapshot{
		ServerKey: "server-a", Complete: true, Target: model.ObserveSession(live), TargetSessionID: "$1",
		Sessions:    []model.SessionIdentity{{Name: "work", LogicalID: live.LogicalID, RuntimeID: "$1"}},
		Objects:     []model.ObjectIdentity{{Kind: model.ObjectSession, LogicalID: live.LogicalID, RuntimeID: "$1"}, {Kind: model.ObjectWindow, RuntimeID: "@1", PaneCount: 1}, {Kind: model.ObjectPane, RuntimeID: "%1", ParentID: "@1"}},
		Memberships: []model.WindowMembership{{SessionID: "$1", WindowID: "@1", Index: 1}},
	}}
	st := &baselineTestStore{baseline: baseline}
	report, err := ReconcileSession(context.Background(), ops, st, "work", false)
	if err == nil || !strings.Contains(err.Error(), "gotomux -f") || report != "" {
		t.Fatalf("incomplete baseline result = report %q, err %v", report, err)
	}
	if ops.sets != 0 || st.started {
		t.Fatalf("reconcile mutated identity or journal before rejecting incomplete baseline: sets=%d started=%v", ops.sets, st.started)
	}
}

func TestReconcileReplansAfterNoopJournalNeedsAttention(t *testing.T) {
	baseline := model.Session{
		Name: "work", LogicalID: "00000000-0000-4000-8000-000000000001", TmuxID: "$1", ServerKey: "server-a",
		SchemaVersion: model.CurrentBaselineSchemaVersion, ClassifierVersion: 1,
		Windows: []model.Window{{Idx: 1, Name: "yazi", Role: "files", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Panes: []model.Pane{{Idx: 1, Cmd: "yazi", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}}},
	}
	snapshot := model.Snapshot{
		ServerKey: "server-a", Complete: true, Target: model.ObserveSession(baseline), TargetSessionID: "$1", ActiveWindowID: "@1",
		Sessions:    []model.SessionIdentity{{Name: "work", LogicalID: baseline.LogicalID, RuntimeID: "$1"}},
		Objects:     []model.ObjectIdentity{{Kind: model.ObjectSession, LogicalID: baseline.LogicalID, RuntimeID: "$1"}, {Kind: model.ObjectWindow, SlotID: baseline.Windows[0].SlotID, RuntimeID: "@1", PaneCount: 1}, {Kind: model.ObjectPane, SlotID: baseline.Windows[0].Panes[0].SlotID, RuntimeID: "%1", ParentID: "@1"}},
		Memberships: []model.WindowMembership{{SessionID: "$1", WindowID: "@1", Index: 1}},
	}
	desired := core.DesiredState{Baseline: model.BaselineFromSession(baseline), ClassifierVersion: 1}
	plan := core.Plan(desired, snapshot, core.Resolve(desired, snapshot), core.PolicyPreserve)
	if plan.Status != core.PlanNoop {
		t.Fatalf("fixture plan = %+v; want noop", plan)
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	old := model.ReconcileJournal{ID: "old-noop", SessionKey: baseline.LogicalID, SessionName: baseline.Name, SessionLogicalID: baseline.LogicalID, Policy: string(core.PolicyPreserve), Status: model.JournalNeedsAttention, PlanJSON: string(planJSON)}
	st := &baselineTestStore{baseline: &baseline, active: &old, journals: map[string]model.ReconcileJournal{old.ID: old}}
	ops := &identifiedTestOps{snapshot: snapshot}
	_, err = ReconcileSession(context.Background(), ops, st, baseline.Name, false)
	if err != nil {
		t.Fatalf("reconcile after stale no-op journal: %v", err)
	}
	if st.journals[old.ID].Status != model.JournalAborted {
		t.Fatalf("stale no-op journal status = %s, want aborted", st.journals[old.ID].Status)
	}
	if st.active == nil || st.active.ID == old.ID || st.active.Status != model.JournalComplete {
		t.Fatalf("replacement journal = %+v, want a newly completed journal", st.active)
	}
}

func TestNeedsAttentionNoopJournalReplansCurrentTaggedLiveState(t *testing.T) {
	baseline := model.Session{
		Name: "work", LogicalID: "00000000-0000-4000-8000-000000000001", TmuxID: "$1", ServerKey: "server-a",
		SchemaVersion: model.CurrentBaselineSchemaVersion, ClassifierVersion: 1,
		Windows: []model.Window{{Idx: 1, Name: "yazi", Role: "files", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000002", TmuxID: "@1", Panes: []model.Pane{{Idx: 1, Cmd: "yazi", Cwd: "/work", SlotID: "00000000-0000-4000-8000-000000000003", TmuxID: "%1"}}}},
	}
	oldSnapshot := model.Snapshot{
		ServerKey: "server-a", Complete: true, Target: model.ObserveSession(baseline), TargetSessionID: "$1", ActiveWindowID: "@1",
		Sessions:    []model.SessionIdentity{{Name: "work", LogicalID: baseline.LogicalID, RuntimeID: "$1"}},
		Objects:     []model.ObjectIdentity{{Kind: model.ObjectSession, LogicalID: baseline.LogicalID, RuntimeID: "$1"}, {Kind: model.ObjectWindow, SlotID: baseline.Windows[0].SlotID, RuntimeID: "@1", PaneCount: 1}, {Kind: model.ObjectPane, SlotID: baseline.Windows[0].Panes[0].SlotID, RuntimeID: "%1", ParentID: "@1"}},
		Memberships: []model.WindowMembership{{SessionID: "$1", WindowID: "@1", Index: 1}},
	}
	desired := core.DesiredState{Baseline: model.BaselineFromSession(baseline), ClassifierVersion: 1}
	oldPlan := core.Plan(desired, oldSnapshot, core.Resolve(desired, oldSnapshot), core.PolicyPreserve)
	if oldPlan.Status != core.PlanNoop {
		t.Fatalf("old plan = %+v; want noop", oldPlan)
	}
	planJSON, err := json.Marshal(oldPlan)
	if err != nil {
		t.Fatal(err)
	}
	old := model.ReconcileJournal{ID: "stale-noop", SessionKey: baseline.LogicalID, SessionName: baseline.Name, SessionLogicalID: baseline.LogicalID, Policy: string(core.PolicyPreserve), Status: model.JournalNeedsAttention, PlanJSON: string(planJSON)}
	st := &baselineTestStore{baseline: &baseline, active: &old, journals: map[string]model.ReconcileJournal{old.ID: old}}
	current := oldSnapshot
	current.Target.Windows = append([]model.Window(nil), oldSnapshot.Target.Windows...)
	current.Target.Windows[0].Name = "fish"
	current.Target.Windows[0].Role = ""
	current.Target.Windows[0].Panes = append([]model.Pane(nil), oldSnapshot.Target.Windows[0].Panes...)
	current.Target.Windows[0].Panes[0].Cmd = "fish"
	ops := &identifiedTestOps{snapshot: current}
	_, err = ReconcileSession(context.Background(), ops, st, baseline.Name, false)
	if err != nil {
		t.Fatalf("reconcile after stale no-op journal: %v", err)
	}
	if st.journals[old.ID].Status != model.JournalAborted {
		t.Fatalf("stale no-op journal status = %s, want aborted", st.journals[old.ID].Status)
	}
	if !st.started || st.active == nil || st.active.ID == old.ID || st.active.Status != model.JournalComplete {
		t.Fatalf("fresh plan was not started and completed after role drift: started=%v active=%+v", st.started, st.active)
	}
	if st.baseline.Windows[0].Role != "files" {
		t.Fatalf("preserve refresh replaced frozen role with live role %q", st.baseline.Windows[0].Role)
	}
}

func TestReconcileRejectsHiddenDaemonSession(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenWithConfig(&config.Config{DataDir: filepath.Join(dir, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = ReconcileSession(context.Background(), noBaselineOps{}, st, tmux.HiddenControlSession, false)
	if err == nil || !strings.Contains(err.Error(), "hidden control session") {
		t.Fatalf("reconcile hidden session error = %v", err)
	}
}

func TestReconcileReportsIncludeRuntimeIDsAndPolicyOutcome(t *testing.T) {
	ambiguous := core.Resolution{Status: core.ResolutionAmbiguous, Ambiguous: []core.Ambiguity{{Baseline: "editor@index-1", Live: []string{"editor@index-2[@9]", "editor@index-3[@10]"}, Reason: "equal evidence"}}}
	report := resolutionReport(ambiguous)
	if !strings.Contains(report, "@9") || !strings.Contains(report, "@10") || !strings.Contains(report, "equal evidence") {
		t.Fatalf("ambiguity report omits runtime candidates: %q", report)
	}
	noop := reconcileReport(core.PlanResult{Status: core.PlanNoop, Policy: core.PolicyPreserve, PreservedWindows: []string{"@9"}, PreservedPanes: []string{"%20"}})
	if !strings.Contains(noop, "already aligned") || !strings.Contains(noop, "preserved 1 extra window") {
		t.Fatalf("preserve no-op report = %q", noop)
	}
	if got := reconcileReport(core.PlanResult{Status: core.PlanNoop, Policy: core.PolicyPreserve}); got != "already aligned" {
		t.Fatalf("plain no-op report = %q, want explicit confirmation", got)
	}
	exact := reconcileReport(core.PlanResult{Status: core.PlanReady, Policy: core.PolicyExact, DesiredBaseline: model.BaselineFromSession(model.Session{Windows: []model.Window{{}, {}}})})
	if !strings.Contains(exact, "hard reconciled") || !strings.Contains(exact, "2 window(s)") {
		t.Fatalf("exact report = %q", exact)
	}
}

func (s *baselineTestStore) GetBaselineState(name string) (*model.Baseline, error) {
	session, err := s.GetBaseline(name)
	if err != nil || session == nil {
		return nil, err
	}
	state := model.BaselineFromSession(*session)
	return &state, nil
}
func (s *baselineTestStore) GetBaselineStateByLogicalID(id string) (*model.Baseline, error) {
	session, err := s.GetBaselineByLogicalID(id)
	if err != nil || session == nil {
		return nil, err
	}
	state := model.BaselineFromSession(*session)
	return &state, nil
}
func (s *baselineTestStore) CommitReconcileState(state model.Baseline, journalID string) error {
	session := state.Session()
	return s.CommitReconcile(&session, journalID)
}
