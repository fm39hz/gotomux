package template

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/reconcile"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type noBaselineOps struct{ reconcile.Executor }

func (noBaselineOps) Freeze(_ context.Context, name string) (*model.Session, error) {
	return &model.Session{Name: name, Cwd: "/tmp", Windows: []model.Window{{Idx: 0, Panes: []model.Pane{{Cwd: "/tmp"}}}}}, nil
}
func (noBaselineOps) ActiveWindow(context.Context, string) (int, error) { return 0, nil }

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

func session(root string, wins ...model.Window) *model.Session {
	return &model.Session{Name: "t", Cwd: root, Windows: wins}
}

func TestPlanRepairsKhoCongWindowWithoutRestartingSurvivors(t *testing.T) {
	root := t.TempDir()
	children := []string{filepath.Join(root, "cong-dlqg"), filepath.Join(root, "kho-dl-mo")}
	for _, child := range children {
		if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := session(root,
		model.Window{Idx: 1, Name: "editor", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "nvim", TmuxID: "%1"}}},
		model.Window{Idx: 2, Name: "command", Cwd: root, Layout: "tiled", TmuxID: "@2", Panes: []model.Pane{
			{Idx: 0, Cwd: children[0], TmuxID: "%2"},
			{Idx: 1, Cwd: children[1], TmuxID: "%3"},
			{Idx: 2, Cwd: root, TmuxID: "%4"},
			{Idx: 3, Cwd: root, Cmd: "make", TmuxID: "%5"},
		}},
		model.Window{Idx: 3, Name: "files", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "yazi", TmuxID: "%6"}}},
		model.Window{Idx: 4, Name: "agent", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "omp", TmuxID: "%7"}}},
	)
	live := session(root,
		model.Window{Idx: 1, Name: "editor", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "nvim", TmuxID: "%1"}}},
		model.Window{Idx: 2, Name: "command", Cwd: root, Layout: "even-horizontal", TmuxID: "@2", Panes: []model.Pane{
			{Idx: 0, Cwd: children[0], TmuxID: "%2"},
			{Idx: 1, Cwd: children[1], TmuxID: "%3"},
			{Idx: 2, Cwd: root, TmuxID: "%4"},
		}},
		model.Window{Idx: 3, Name: "files", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "yazi", TmuxID: "%6"}}},
		model.Window{Idx: 4, Name: "agent", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: root, Cmd: "omp", TmuxID: "%7"}}},
	)
	base.ServerKey, live.ServerKey = "srv", "srv"
	p := reconcile.Build(base, live, 2)
	if len(p.Ambiguous) != 0 || len(p.Creates) != 0 || len(p.Repairs) != 1 {
		t.Fatalf("plan = %+v; want one non-destructive repair", p)
	}
	r := p.Repairs[0]
	if r.Index != 2 || r.WindowID != "@2" || len(r.MissingPanes) != 1 || r.MissingPanes[0].Cmd != "make" || r.Layout != "tiled" {
		t.Fatalf("repair = %+v", r)
	}
	if len(p.Moves) != 0 {
		t.Fatalf("no windows need moving, got %+v", p.Moves)
	}
}

func TestPlanUsesWindowIDsAcrossRenameAndRenumber(t *testing.T) {
	base := session("/work", model.Window{Idx: 1, Name: "editor", TmuxID: "@11", Panes: []model.Pane{{Cmd: "nvim", TmuxID: "%11"}}})
	live := session("/work", model.Window{Idx: 0, Name: "renamed", TmuxID: "@11", Panes: []model.Pane{{Cmd: "nvim", TmuxID: "%11"}}})
	base.ServerKey, live.ServerKey = "server-generation", "server-generation"
	p := reconcile.Build(base, live, 0)
	if len(p.Ambiguous) != 0 || len(p.Matched) != 1 || !p.Matched[0].ByRuntime {
		t.Fatalf("runtime identity match = %+v", p)
	}
	if len(p.Moves) != 2 || p.Moves[0].WindowID != "@11" || p.Moves[1].WindowID != "@11" {
		t.Fatalf("moves should address stable window id: %+v", p.Moves)
	}
	if len(p.Repairs) != 1 || p.Repairs[0].RenameTo != "editor" {
		t.Fatalf("rename repair = %+v", p.Repairs)
	}
}

func TestPlanNameAloneCannotMatchWindow(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, path := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := session(root, model.Window{Idx: 0, Name: "command", Cwd: a, Panes: []model.Pane{{Cwd: a, Cmd: "nvim"}}})
	live := session(root, model.Window{Idx: 0, Name: "command", Cwd: b, Panes: []model.Pane{{Cwd: b, Cmd: "yazi"}}})
	p := reconcile.Build(base, live, 0)
	if len(p.Matched) != 0 || len(p.Creates) != 0 || len(p.Ambiguous) == 0 || p.NeedsApply {
		t.Fatalf("same name alone must leave the candidate unresolved: %+v", p)
	}
}

func TestRuntimeWindowIDMismatchCannotBecomeSemanticMatch(t *testing.T) {
	root := t.TempDir()
	base := session(root, model.Window{Idx: 0, Name: "editor", Cwd: root, TmuxID: "@1", Panes: []model.Pane{{Cwd: root, Cmd: "nvim"}}})
	live := session(root, model.Window{Idx: 0, Name: "editor", Cwd: root, TmuxID: "@9", Panes: []model.Pane{{Cwd: root, Cmd: "yazi"}}})
	base.ServerKey, live.ServerKey = "same-server", "same-server"
	p := reconcile.Build(base, live, 0)
	if len(p.Matched) != 0 || len(p.Ambiguous) != 0 || len(p.Missing) != 1 || len(p.Extra) != 1 {
		t.Fatalf("different live @window-id must not match by name/shape: %+v", p)
	}
}

func TestPlanSkipsWindowNameTmuxWillNotApply(t *testing.T) {
	root := t.TempDir()
	base := session(root, model.Window{Idx: 0, Name: "s", Cwd: root, TmuxID: "@3", Panes: []model.Pane{{Cwd: root, Cmd: "nvim", TmuxID: "%4"}}})
	live := session(root, model.Window{Idx: 0, Name: "editor", Cwd: root, TmuxID: "@3", Panes: []model.Pane{{Cwd: root, Cmd: "nvim", TmuxID: "%4"}}})
	base.Name, live.Name = "s", "s"
	base.ServerKey, live.ServerKey = "srv", "srv"
	p := reconcile.Build(base, live, 0)
	if len(p.Repairs) != 0 || p.NeedsApply {
		t.Fatalf("unapplicable session-name window should not create a futile repair: %+v", p)
	}
}

func TestPlanPairsWindowsGloballyByStableProjectScope(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, path := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := session(root,
		model.Window{Idx: 0, Name: "a", Cwd: a, Panes: []model.Pane{{Cwd: a}}},
		model.Window{Idx: 1, Name: "b", Cwd: b, Panes: []model.Pane{{Cwd: b}}},
	)
	live := session(root,
		model.Window{Idx: 0, Name: "unknown-1", Cwd: b, Panes: []model.Pane{{Cwd: b}}},
		model.Window{Idx: 1, Name: "unknown-2", Cwd: a, Panes: []model.Pane{{Cwd: a}}},
	)
	p := reconcile.Build(base, live, 0)
	if len(p.Ambiguous) != 0 || len(p.Matched) != 2 || p.Matched[0].LiveIndex != 1 || p.Matched[1].LiveIndex != 0 {
		t.Fatalf("global assignment = %+v", p)
	}
}

func TestPlanDoesNotTreatChangedCwdAsMissingPane(t *testing.T) {
	root := t.TempDir()
	base := session(root, model.Window{Idx: 0, Name: "shell", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: root}}})
	live := session(root, model.Window{Idx: 0, Name: "shell", Cwd: root, Panes: []model.Pane{{Idx: 0, Cwd: filepath.Join(root, "src")}}})
	p := reconcile.Build(base, live, 0)
	if len(p.Ambiguous) != 0 || len(p.Creates) != 0 || len(p.Repairs) != 0 {
		t.Fatalf("ordinary cwd change must remain untouched: %+v", p)
	}
}

func TestPlanExactToolSurvivesProjectScopeChange(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, path := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := session(root, model.Window{Idx: 0, Name: "editor", Cwd: a, Panes: []model.Pane{{Idx: 0, Cwd: a, Cmd: "nvim"}}})
	live := session(root, model.Window{Idx: 0, Name: "editor", Cwd: b, Panes: []model.Pane{{Idx: 0, Cwd: b, Cmd: "nvim"}}})
	p := reconcile.Build(base, live, 0)
	if len(p.Matched) != 1 || p.Matched[0].BaseIndex != 0 || len(p.Creates) != 0 || len(p.Repairs) != 0 {
		t.Fatalf("tool identity must survive scope change: %+v", p)
	}
}

func TestPlanRepairsSplitTreeWhenLayoutClassIsUnchanged(t *testing.T) {
	root := t.TempDir()
	baseLayout := "aaaa,100x30,0,0{49x30,0,0,1,50x30,50,0{24x30,50,0,2,25x30,75,0,3}}"
	liveLayout := "cccc,100x30,0,0{49x30,0,0{24x30,0,0,1,25x30,25,0,2},50x30,50,0,3}"
	base := session(root, model.Window{Idx: 0, Name: "shell", Cwd: root, Layout: baseLayout, Panes: []model.Pane{{Idx: 0, Cwd: root}, {Idx: 1, Cwd: root}, {Idx: 2, Cwd: root}}})
	live := session(root, model.Window{Idx: 0, Name: "shell", Cwd: root, Layout: liveLayout, Panes: []model.Pane{{Idx: 0, Cwd: root}, {Idx: 1, Cwd: root}, {Idx: 2, Cwd: root}}})
	p := reconcile.Build(base, live, 0)
	if len(p.Ambiguous) != 0 || len(p.Repairs) != 1 || p.Repairs[0].Layout != baseLayout {
		t.Fatalf("changed split tree should restore baseline dump: %+v", p)
	}
}

func TestPlanReportsUnresolvableDuplicateShellWindows(t *testing.T) {
	root := t.TempDir()
	base := session(root,
		model.Window{Idx: 0, Name: "one", Cwd: root, Panes: []model.Pane{{Cwd: root}}},
		model.Window{Idx: 1, Name: "two", Cwd: root, Panes: []model.Pane{{Cwd: root}}},
	)
	live := session(root,
		model.Window{Idx: 0, Name: "x", Cwd: root, Panes: []model.Pane{{Cwd: root}}},
		model.Window{Idx: 1, Name: "y", Cwd: root, Panes: []model.Pane{{Cwd: root}}},
	)
	p := reconcile.Build(base, live, 0)
	if len(p.Ambiguous) == 0 || p.NeedsApply {
		t.Fatalf("indistinguishable identities must stop mutation: %+v", p)
	}
}

func TestPlanPreservesExtraWindowOutsideBaselineRange(t *testing.T) {
	root := t.TempDir()
	base := session(root, model.Window{Idx: 0, Name: "editor", Cwd: root, Panes: []model.Pane{{Cmd: "nvim", Cwd: root}}})
	live := session(root,
		model.Window{Idx: 0, Name: "editor", Cwd: root, Panes: []model.Pane{{Cmd: "nvim", Cwd: root}}},
		model.Window{Idx: 2, Name: "extra", Cwd: root, Panes: []model.Pane{{Cmd: "htop", Cwd: root}}},
	)
	p := reconcile.Build(base, live, 2)
	if len(p.Ambiguous) != 0 || len(p.Extra) != 1 || p.Extra[0] != 1 || p.NeedsApply {
		t.Fatalf("extra outside target range must remain untouched: %+v", p)
	}
}
