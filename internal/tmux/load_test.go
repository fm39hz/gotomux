package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmuxtest"
)

// mirrors tmuxp/dotnet-grimoire-net.json:
//
//	w0 editor: 1 pane nvim @ root
//	w1 test:   2 panes shell @ root and root/test
func TestLoadGrimoireShape(t *testing.T) {
	tmp := tmuxtest.Isolate(t)
	ctl, err := New()
	if err != nil {
		t.Fatal(err)
	}
	name := "tp-test-grimoire"
	defer func() { _ = ctl.Kill(context.Background(), name) }()

	root := filepath.Join(tmp, "grimoire")
	testDir := root + "/test"
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &model.Session{
		Name: name,
		Cwd:  root,
		Windows: []model.Window{
			{
				Name: "editor",
				Cwd:  root,
				Panes: []model.Pane{
					{Idx: 1, Cwd: root, Cmd: "nvim"},
				},
			},
			{
				Name: "test",
				Cwd:  root,
				Panes: []model.Pane{
					{Idx: 1, Cwd: root, Cmd: ""},
					{Idx: 2, Cwd: testDir, Cmd: ""},
				},
			},
		},
	}
	if err := ctl.Load(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !ctl.Has(context.Background(), name) {
		t.Fatal("session missing")
	}
	time.Sleep(300 * time.Millisecond)

	out, err := exec.Command("tmux", "list-windows", "-t", name, "-F", "#{window_name}:#{window_panes}").Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 windows, got %q", string(out))
	}
	if lines[0] != "editor:1" {
		t.Fatalf("win0: %q want editor:1", lines[0])
	}
	if lines[1] != "test:2" {
		t.Fatalf("win1: %q want test:2", lines[1])
	}

	// only this session - no -a
	out, err = exec.Command("tmux", "list-panes", "-s", "-t", name,
		"-F", "#{window_name}|#{pane_index}|#{pane_current_path}|#{pane_current_command}|#{pane_start_command}|#{pane_pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + string(out))

	var editorNvim, testRoot, testSub bool
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 6 {
			continue
		}
		win, _, path, cur, start, pidStr := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
		pid, _ := strconv.Atoi(pidStr)
		tool := detectPaneCmd(cur, start, int32(pid), loadProcIndex())
		if tool == "" && start != "" {
			tool = binBase(start)
		}
		switch win {
		case "editor":
			if tool == "nvim" || cur == "nvim" || start == "nvim" {
				editorNvim = true
			}
			if path != root {
				t.Errorf("editor cwd=%s want %s", path, root)
			}
		case "test":
			if path == root {
				testRoot = true
			}
			if path == testDir {
				testSub = true
			}
		}
	}
	if !editorNvim {
		t.Error("editor pane not running nvim (check child of nu -c)")
	}
	if !testRoot || !testSub {
		t.Errorf("test panes cwd missing: root=%v sub=%v", testRoot, testSub)
	}
}

// mirrors tmuxp/kho-cong.json shape
func TestLoadKhoCongShape(t *testing.T) {
	tmp := tmuxtest.Isolate(t)
	ctl, err := New()
	if err != nil {
		t.Fatal(err)
	}
	name := "tp-test-kho"
	defer func() { _ = ctl.Kill(context.Background(), name) }()

	root := filepath.Join(tmp, "kho")
	a, b := root+"/cong-dlqg", root+"/kho-dl-mo"
	for _, path := range []string{a, b} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := &model.Session{
		Name: name,
		Cwd:  root,
		Windows: []model.Window{
			{Name: "code", Cwd: root, Panes: []model.Pane{{Cwd: root, Cmd: "nvim"}}},
			{Name: "shell", Cwd: root, Panes: []model.Pane{{Cwd: a}, {Cwd: b}}},
			{Name: "files", Cwd: root, Panes: []model.Pane{{Cwd: root, Cmd: "yazi"}}},
		},
	}
	if err := ctl.Load(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	out, err := exec.Command("tmux", "list-windows", "-t", name, "-F", "#{window_name}:#{window_panes}").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(out))
	t.Log("windows:\n" + got)
	want := []string{"code:1", "shell:2", "files:1"}
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 windows, got %q", got)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("win[%d]=%q want %q", i, lines[i], w)
		}
	}

	out, _ = exec.Command("tmux", "list-panes", "-s", "-t", name,
		"-F", "#{window_name}|#{pane_current_path}|#{pane_current_command}|#{pane_start_command}|#{pane_pid}").Output()
	t.Log("panes:\n" + string(out))

	var hasNvim, hasYazi, hasA, hasB bool
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		win, path, cur, start, pidStr := parts[0], parts[1], parts[2], parts[3], parts[4]
		pid, _ := strconv.Atoi(pidStr)
		tool := detectPaneCmd(cur, start, int32(pid), loadProcIndex())
		if tool == "" {
			tool = binBase(start)
		}
		if path == a {
			hasA = true
		}
		if path == b {
			hasB = true
		}
		if win == "code" && (tool == "nvim" || start == "nvim") {
			hasNvim = true
		}
		if win == "files" && (tool == "yazi" || start == "yazi") {
			hasYazi = true
		}
	}
	if !hasNvim {
		t.Error("missing nvim on kho-cong")
	}
	if !hasYazi {
		t.Error("missing yazi on files")
	}
	if !hasA || !hasB {
		t.Errorf("shell pane paths: a=%v b=%v", hasA, hasB)
	}
}

// Freeze often stores a middle window named like the session (cwd basename).
// new-window -t bare name then fails with "index N in use".
func TestLoadWindowNamedLikeSession(t *testing.T) {
	tmp := tmuxtest.Isolate(t)
	ctl, err := New()
	if err != nil {
		t.Fatal(err)
	}
	name := "tp-test-ambig-name"
	defer func() { _ = ctl.Kill(context.Background(), name) }()

	root := filepath.Join(tmp, "ambiguous")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &model.Session{
		Name: name,
		Cwd:  root,
		Windows: []model.Window{
			{Name: "nvim", Cwd: root, Panes: []model.Pane{{Cwd: root}}},
			{Name: name, Cwd: root, Panes: []model.Pane{{Cwd: root}}},
			{Name: "pi", Cwd: root, Panes: []model.Pane{{Cwd: root}}},
		},
	}
	if err := ctl.Load(context.Background(), p); err != nil {
		t.Fatalf("load with window==session name: %v", err)
	}
	out, err := exec.Command("tmux", "list-windows", "-t", name, "-F", "#{window_index}:#{window_name}").Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 windows, got %q", string(out))
	}
}

// Replay contract: rebuilding a frozen session must not depend on the launch
// environment. Production spawns panes from the tmux server's environment
// (systemd-minimal when gotomuxd owns the server), where a bare tool name the
// user's interactive shell resolves fine (config-added PATH entries) is NOT
// found: the pane dies within milliseconds, remain-on-exit closes the window,
// and the rebuilt session silently loses it — reported as "rerun shows the old
// layout". Freeze therefore records the resolved executable path and Load
// replays that.
func TestFreezeReplaySurvivesLaunchEnv(t *testing.T) {
	root := tmuxtest.Isolate(t)
	_ = root
	ctl, err := New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := "tp-pathreplay"
	defer func() { _ = ctl.Kill(ctx, name) }()

	// A tool installed OUTSIDE every PATH dir — replaying its bare name fails
	// no matter what the server PATH is; only the absolute path works.
	toolDir := t.TempDir()
	absTool := filepath.Join(toolDir, "faketool")
	if err := os.WriteFile(absTool, []byte("#!/bin/sh\nsleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := &model.Session{
		Name: name,
		Windows: []model.Window{
			{Name: "editor", Cwd: "/tmp", Panes: []model.Pane{{Cmd: "/bin/cat"}}},
			{Name: "agent", Cwd: "/tmp", Panes: []model.Pane{{Cmd: absTool}}},
		},
	}
	if err := ctl.Load(ctx, orig); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	frozen, err := ctl.Freeze(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctl.Kill(ctx, name); err != nil {
		t.Fatal(err)
	}
	// Rebuild from the frozen preset — ConnectPreset's Load half.
	if err := ctl.Load(ctx, frozen); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	out, err := exec.Command("tmux", "list-windows", "-t", name, "-F", "#{window_name}").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(out))
	if got != "editor\nagent" {
		t.Fatalf("rebuilt session lost a window: windows = %q, want editor\\nagent (tool replayed)", got)
	}
}
