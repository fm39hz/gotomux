package template

import (
	"context"
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
	return &model.Snapshot{ServerKey: session.ServerKey, Complete: true, Target: session, TargetSessionID: session.TmuxID, Sessions: []model.SessionIdentity{{Name: name, RuntimeID: session.TmuxID}}, Objects: []model.ObjectIdentity{{Kind: model.ObjectSession, RuntimeID: session.TmuxID}}}, nil
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
}

func (s *baselineTestStore) GetBaselineByLogicalID(string) (*model.Session, error) {
	return s.baseline, nil
}

func (*baselineTestStore) GetActiveReconcile(string) (*model.ReconcileJournal, error) {
	return nil, nil
}

func (s *baselineTestStore) StartReconcile(model.ReconcileJournal) error {
	s.started = true
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
		ServerKey: "server-a", Complete: true, Target: live, TargetSessionID: "$1",
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
	exact := reconcileReport(core.PlanResult{Status: core.PlanReady, Policy: core.PolicyExact, DesiredSession: model.Session{Windows: []model.Window{{}, {}}}})
	if !strings.Contains(exact, "hard reconciled") || !strings.Contains(exact, "2 window(s)") {
		t.Fatalf("exact report = %q", exact)
	}
}
