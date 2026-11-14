package classify

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func TestWindowRoleNormalizesToolAndCanonicalNameAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  string
		want string
	}{
		{"yazi", "yazi", "files"},
		{"yazi", "sleep", "files"},
		{"codex", "nvim", "editor"},
		{"codex", "sleep", "agent"},
		{"agent", "omp", "agent"},
		{"misc", "fish", ""},
	} {
		if got := WindowRole(model.Window{Name: tc.name, Panes: []model.Pane{{Cmd: tc.cmd}}}); got != tc.want {
			t.Errorf("WindowRole(%q,%q) = %q, want %q", tc.name, tc.cmd, got, tc.want)
		}
	}
}

func TestWindowRoleDoesNotCollapseMixedToolWindow(t *testing.T) {
	w := model.Window{Name: "workspace", Panes: []model.Pane{{Cmd: "nvim"}, {Cmd: "yazi"}}}
	if got := WindowRole(w); got != "" {
		t.Fatalf("mixed-tool window role = %q, want unknown", got)
	}
}
