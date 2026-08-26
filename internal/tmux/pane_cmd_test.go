package tmux

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

// paneCmd replay policy: the frozen absolute executable wins while it still
// exists; a dangling path (tool moved/upgraded since freeze) degrades to the
// pre-CmdPath behaviour instead of failing the spawn.
func TestPaneCmdPrefersLiveCmdPathAndFallsBackWhenDangling(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "omp")
	if err := os.WriteFile(live, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		p    model.Pane
		want string
	}{
		{
			name: "live path keeps start arguments",
			p:    model.Pane{Cmd: "omp", CmdPath: live, StartCmd: "omp --serve"},
			want: live + " --serve",
		},
		{
			name: "live path without start command",
			p:    model.Pane{Cmd: "omp", CmdPath: live},
			want: live,
		},
		{
			name: "dangling path falls back to start command",
			p:    model.Pane{Cmd: "omp", CmdPath: filepath.Join(dir, "gone"), StartCmd: "omp --serve"},
			want: "omp --serve",
		},
		{
			name: "dangling path without start command falls back to bare cmd",
			p:    model.Pane{Cmd: "omp", CmdPath: filepath.Join(dir, "gone")},
			want: "omp",
		},
		{
			name: "no cmdpath keeps start command",
			p:    model.Pane{Cmd: "omp", StartCmd: "omp --serve"},
			want: "omp --serve",
		},
		{
			name: "nothing detected keeps bare cmd",
			p:    model.Pane{Cmd: "nu"},
			want: "nu",
		},
	}
	for _, tc := range cases {
		if got := paneCmd(tc.p); got != tc.want {
			t.Errorf("%s: paneCmd = %q, want %q", tc.name, got, tc.want)
		}
	}
}
