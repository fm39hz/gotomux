package template

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func w(idx int, cmd string) model.Window {
	return model.Window{Idx: idx, Panes: []model.Pane{{Cmd: cmd}}}
}

func sess(wins ...model.Window) *model.Session {
	s := &model.Session{Name: "t", Cwd: "/tmp"}
	s.Windows = wins
	return s
}

// The user's real shape: editor -> shell -> agent (nvim, shell, omp) at
// base-index 1. Restoring an already-canonical session must be a no-op, and
// restoring after the editor died must rebuild exactly the canonical order.
func TestPlanResetCanonicalShapeNoop(t *testing.T) {
	preset := sess(w(1, "nvim"), w(2, ""), w(3, "omp"))
	p := planReset(preset, preset, 1)
	if len(p.moves) != 0 || len(p.creates) != 0 || len(p.extras) != 0 {
		t.Fatalf("canonical live must plan nothing, got %+v", p)
	}
}

func TestPlanResetRebuildsEditorNotMovesItToTail(t *testing.T) {
	// Baseline (preset): editor@1 nvim, shell@2, agent@3 omp.
	// Live after the editor window died (renumbered): shell@1, agent@2.
	preset := sess(w(1, "nvim"), w(2, ""), w(3, "omp"))
	live := sess(w(1, ""), w(2, "omp"))
	p := planReset(preset, live, 1)
	if len(p.creates) != 1 || p.creates[0].idx != 1 || p.creates[0].w.Panes[0].Cmd != "nvim" {
		t.Fatalf("creates = %+v, want nvim recreated at index 1", p.creates)
	}
	if p.placed != 2 {
		t.Errorf("placed = %d, want 2", p.placed)
	}
	if len(p.extras) != 0 {
		t.Errorf("extras = %+v, want none", p.extras)
	}
	// Survivors must land on their baseline slots (shell@2, agent@3) — never
	// on a tail that would put editor last.
	if len(p.moves) != 4 {
		t.Fatalf("moves = %d, want 4 (2 vacate + 2 place)", len(p.moves))
	}
}

func TestPlanResetRebuildsDeadWindowAndRenumbers(t *testing.T) {
	// Baseline: nvim, shell, shell. The nvim window died and its exit made
	// tmux renumber the two surviving shell windows down to 0 and 1.
	base := sess(w(0, "nvim"), w(1, ""), w(2, ""))
	live := sess(w(0, ""), w(1, ""))

	p := planReset(base, live, 0)
	if len(p.creates) != 1 || p.creates[0].idx != 0 {
		t.Fatalf("creates = %+v, want one at idx 0", p.creates)
	}
	if got := p.creates[0].w.Panes[0].Cmd; got != "nvim" {
		t.Errorf("recreated window cmd = %q, want nvim", got)
	}
	if p.placed != 2 {
		t.Errorf("placed = %d, want 2", p.placed)
	}
	if len(p.extras) != 0 {
		t.Errorf("extras = %+v, want none", p.extras)
	}
	if p.activeTo != 1 {
		// active window is the shell that used to be window 0 -> baseline 1
		t.Errorf("activeTo = %d, want 1", p.activeTo)
	}
	// Execution order: vacate moves first, so every later target is free.
	if len(p.moves) != 4 {
		t.Fatalf("moves = %d, want 4 (2 vacate + 2 place)", len(p.moves))
	}
	if p.moves[0][0] > p.moves[0][1] {
		t.Errorf("first move %v must be a vacate (upward)", p.moves[0])
	}
}

func TestPlanResetNoopWhenExact(t *testing.T) {
	base := sess(w(0, "nvim"), w(1, ""))
	p := planReset(base, base, 1)
	if len(p.moves) != 0 || len(p.creates) != 0 || len(p.extras) != 0 {
		t.Fatalf("exact match must produce an empty plan, got %+v", p)
	}
}

func TestPlanResetKeepsExtraBeyondBaseline(t *testing.T) {
	// A window the user added after the baseline sits above the baseline
	// zone untouched; the matching survivor goes back to its baseline slot.
	base := sess(w(0, "nvim"))
	live := sess(w(0, "nvim"), w(2, "htop"))
	p := planReset(base, live, 2)
	if len(p.creates) != 0 {
		t.Errorf("creates = %+v, want none", p.creates)
	}
	if len(p.extras) != 0 {
		t.Errorf("extras = %+v, want none (extra already beyond baseline)", p.extras)
	}
	if p.placed != 1 {
		t.Errorf("placed = %d, want 1", p.placed)
	}
	if p.activeTo != -1 {
		// the active extra never moves, so tmux keeps it current — no select
		t.Errorf("activeTo = %d, want -1 (untouched active extra)", p.activeTo)
	}
}

func TestPlanResetMovesInZoneExtraToTail(t *testing.T) {
	// The user replaced window 1 with a different tool; it is an extra living
	// inside the baseline zone and must be pushed out past the baseline.
	base := sess(w(0, "nvim"), w(1, ""))
	live := sess(w(0, ""), w(1, "htop"))
	p := planReset(base, live, 1)
	if len(p.creates) != 1 {
		t.Fatalf("creates = %+v, want nvim recreated", p.creates)
	}
	if len(p.extras) != 1 {
		t.Fatalf("extras = %+v, want htop moved to tail", p.extras)
	}
	if p.extras[0][1] != 2 {
		t.Errorf("extra target = %d, want 2", p.extras[0][1])
	}
	if p.activeTo != 2 {
		t.Errorf("activeTo = %d, want 2 (active htop kept active)", p.activeTo)
	}
}

func TestPlanResetAdoptsNothingOnEmptyBaseline(t *testing.T) {
	p := planReset(&model.Session{Name: "t"}, sess(w(0, ""), w(1, "")), 0)
	if len(p.moves) != 0 || len(p.creates) != 0 || len(p.extras) != 0 {
		t.Fatalf("empty baseline must plan nothing, got %+v", p)
	}
}

func TestPlanResetRepairsModifiedKhoCongWindow(t *testing.T) {
	root := "/work/kho-cong"
	base := sess(
		model.Window{Idx: 1, Name: "editor", Panes: []model.Pane{{Cmd: "nvim"}}},
		model.Window{Idx: 2, Name: "command", Layout: "tiled", Panes: []model.Pane{
			{Cwd: root + "/cong-dlqg"},
			{Cwd: root + "/kho-dl-mo"},
			{Cwd: root},
			{Cwd: root, Cmd: "make"},
		}},
		model.Window{Idx: 3, Name: "files", Panes: []model.Pane{{Cmd: "yazi"}}},
		model.Window{Idx: 4, Name: "agent", Panes: []model.Pane{{Cmd: "omp"}}},
	)
	live := sess(
		model.Window{Idx: 1, Name: "editor", Panes: []model.Pane{{Cmd: "nvim"}}},
		model.Window{Idx: 2, Name: "command", Layout: "even-horizontal", Panes: []model.Pane{
			{Cwd: root + "/cong-dlqg"},
			{Cwd: root + "/kho-dl-mo"},
			{Cwd: root},
		}},
		model.Window{Idx: 3, Name: "files", Panes: []model.Pane{{Cmd: "yazi"}}},
		model.Window{Idx: 4, Name: "agent", Panes: []model.Pane{{Cmd: "omp"}}},
	)

	p := planReset(base, live, 2)
	if len(p.creates) != 0 {
		t.Fatalf("modified live window must be repaired, not recreated: %+v", p.creates)
	}
	if len(p.repairs) != 1 {
		t.Fatalf("repairs = %+v, want one command-window repair", p.repairs)
	}
	r := p.repairs[0]
	if r.idx != 2 || len(r.missing) != 1 || r.missing[0].Cmd != "make" {
		t.Fatalf("repair = %+v, want missing make pane at window 2", r)
	}
	if r.layout != "tiled" {
		t.Fatalf("repair layout = %q, want tiled", r.layout)
	}
}

func TestExactMatchChecksFullWindowTopology(t *testing.T) {
	base := sess(
		model.Window{Idx: 1, Name: "command", Layout: "tiled", Panes: []model.Pane{{}, {}, {}, {Cmd: "make"}}},
	)
	live := sess(
		model.Window{Idx: 1, Name: "command", Layout: "tiled", Panes: []model.Pane{{}, {}, {}}},
	)
	if exactMatch(base, live) {
		t.Fatal("window with a missing pane must not be treated as an exact match")
	}
}

func TestPlanResetMatchesRenamedWindowFromTopology(t *testing.T) {
	base := sess(
		model.Window{Idx: 1, Name: "command", Layout: "tiled", Panes: []model.Pane{
			{Cwd: "/work/a"}, {Cwd: "/work/b"}, {Cmd: "make", Cwd: "/work"},
		}},
	)
	live := sess(
		model.Window{Idx: 1, Name: "wrong-name", Layout: "tiled", Panes: []model.Pane{
			{Cwd: "/work/a"}, {Cwd: "/work/b"}, {Cmd: "make", Cwd: "/work"},
		}},
	)
	p := planReset(base, live, 1)
	if len(p.creates) != 0 || len(p.repairs) != 1 {
		t.Fatalf("renamed window should match by topology and repair name: %+v", p)
	}
	if !p.repairs[0].rename || p.repairs[0].name != "command" {
		t.Fatalf("repair = %+v, want rename to command", p.repairs[0])
	}
}

func TestPlanResetDoesNotMatchByNameAlone(t *testing.T) {
	base := sess(model.Window{Idx: 1, Name: "command", Panes: []model.Pane{{Cmd: "nvim", Cwd: "/work"}}})
	live := sess(model.Window{Idx: 1, Name: "command", Panes: []model.Pane{{Cmd: "yazi", Cwd: "/other"}}})
	p := planReset(base, live, 1)
	if len(p.creates) != 1 || len(p.repairs) != 0 {
		t.Fatalf("same name alone must not identify a window: %+v", p)
	}
}

func TestPlanResetDoesNotUseNameForDifferentShellTopology(t *testing.T) {
	base := sess(model.Window{Idx: 1, Name: "command", Layout: "tiled", Panes: []model.Pane{{}, {}, {}}})
	live := sess(model.Window{Idx: 1, Name: "command", Layout: "even-horizontal", Panes: []model.Pane{{}, {}, {}}})
	p := planReset(base, live, 1)
	if len(p.creates) != 1 || len(p.repairs) != 0 {
		t.Fatalf("same name/count without topology evidence must not match: %+v", p)
	}
}
