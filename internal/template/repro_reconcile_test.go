package template

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// ── Defect #2 fix verification: bag-of-tools matching ───────────────────

func TestReproDuplicateAgent_ReorderedPanes(t *testing.T) {
	// Baseline: [shell, claude]
	// Live:     [claude, shell]  — reordered, same tools
	base := sess(model.Window{
		Idx: 0, Name: "agent",
		Panes: []model.Pane{{Cmd: ""}, {Cmd: "claude"}},
	})
	live := sess(model.Window{
		Idx: 0, Name: "agent",
		Panes: []model.Pane{{Cmd: "claude"}, {Cmd: ""}},
	})

	p := planReset(base, live, 0)

	// After fix: bag matching sees same tool set → exactMatch or repair, not create.
	if len(p.creates) != 0 {
		t.Errorf("creates = %d, want 0 (reordered panes must not trigger recreate)", len(p.creates))
	}
}

func TestReproDuplicateAgent_OnePaneDiedShifted(t *testing.T) {
	// Baseline: [shell, claude]
	// Live:     [claude]         — shell died, claude shifted to index 0
	base := sess(model.Window{
		Idx: 0, Name: "agent",
		Panes: []model.Pane{{Cmd: ""}, {Cmd: "claude"}},
	})
	live := sess(model.Window{
		Idx: 0, Name: "agent",
		Panes: []model.Pane{{Cmd: "claude"}},
	})

	p := planReset(base, live, 0)

	// After fix: window matches via bag-of-tools (claude found in both).
	// repairWindow finds only the shell pane is genuinely missing.
	if len(p.repairs) != 1 {
		t.Fatalf("repairs = %d, want 1", len(p.repairs))
	}
	r := p.repairs[0]
	if len(r.missing) != 1 {
		t.Fatalf("missing = %d, want 1", len(r.missing))
	}
	// The missing pane must be the shell (empty cmd), NOT claude.
	if r.missing[0].Cmd != "" {
		t.Errorf("missing[0].Cmd = %q, want empty (shell); claude is alive and must not be duplicated", r.missing[0].Cmd)
	}
}

func TestReproMissingNvim_WrongTailSlice(t *testing.T) {
	// Baseline: [nvim, shell, make]
	// Live:     [nvim, make]    — shell exited, make shifted left
	base := sess(model.Window{
		Idx: 0, Name: "editor",
		Panes: []model.Pane{{Cmd: "nvim"}, {Cmd: ""}, {Cmd: "make"}},
	})
	live := sess(model.Window{
		Idx: 0, Name: "editor",
		Panes: []model.Pane{{Cmd: "nvim"}, {Cmd: "make"}},
	})

	p := planReset(base, live, 0)

	// After fix: bag match finds nvim+make in both windows → window matches.
	// Only the shell pane (cmd="") is genuinely missing.
	if len(p.repairs) != 1 {
		t.Fatalf("repairs = %d, want 1", len(p.repairs))
	}
	r := p.repairs[0]
	if len(r.missing) != 1 {
		t.Fatalf("missing = %d, want 1", len(r.missing))
	}
	got := r.missing[0].Cmd
	if got != "" {
		t.Errorf("missing[0].Cmd = %q, want empty (shell); make is alive at live[1] and must not be duplicated", got)
	}
}

// ── ToolIntent sanity check ─────────────────────────────────────────────

func TestReproToolIntentUnknownBinary(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"nvim", "nvim"},
		{"claude", "claude"},
		{"yazi", "yazi"},
		{"bash", ""},
		{"nu", ""},
		{"godot", "godot"},
		{"dotnet", "dotnet"},
	}
	for _, tc := range cases {
		got := tmux.ToolIntent(tc.cmd)
		if got != tc.want {
			t.Errorf("ToolIntent(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}
