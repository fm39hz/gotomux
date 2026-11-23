package tmux

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

// Explicit launch evidence is independent of the observed foreground tool.
func TestPaneCmdPrefersLiveCmdPathAndFallsBackWhenDangling(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "codex")
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
			p:    model.Pane{Cmd: "codex", CmdPath: live, StartCmd: "codex --serve"},
			want: strconv.Quote(live) + " \"--serve\"",
		},
		{
			name: "live path without start command",
			p:    model.Pane{Cmd: "codex", CmdPath: live},
			want: live,
		},
		{
			name: "dangling path falls back to start command",
			p:    model.Pane{Cmd: "codex", CmdPath: filepath.Join(dir, "gone"), StartCmd: "codex --serve"},
			want: "codex --serve",
		},
		{
			name: "dangling path without start command falls back to bare cmd",
			p:    model.Pane{Cmd: "codex", CmdPath: filepath.Join(dir, "gone")},
			want: "codex",
		},
		{
			name: "no cmdpath keeps start command",
			p:    model.Pane{Cmd: "codex", StartCmd: "codex --serve"},
			want: "codex --serve",
		},
		{
			name: "shell observation uses default shell",
			p:    model.Pane{Cmd: "nu"},
			want: "",
		},
	}
	for _, tc := range cases {
		if got := paneCmd(tc.p); got != tc.want {
			t.Errorf("%s: paneCmd = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestReplayDoesNotPromoteTransientForegroundProcesses(t *testing.T) {
	for _, cmd := range []string{"git", "make", "bun", "node", "python3"} {
		if got := paneCmd(model.Pane{Cmd: cmd, CmdPath: "/usr/bin/" + cmd}); got != "" {
			t.Errorf("transient %s became launch command %q", cmd, got)
		}
	}
	if got := paneCmd(model.Pane{Cmd: "git", CmdPath: "/usr/bin/git", StartCmd: "bash -l"}); got != "bash -l" {
		t.Fatalf("foreground replaced launcher: %q", got)
	}
	if got := replayExecutablePath("/usr/bin/nvim (deleted)"); got != "/usr/bin/nvim" {
		t.Fatal(got)
	}
}
