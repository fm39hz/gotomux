package tmux

import (
	"os/exec"

	"github.com/fm39hz/gotomux/internal/store"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mirrors tmuxp/dotnet-grimoire-net.json:
//
//	w0 editor: 1 pane nvim @ root
//	w1 test:   2 panes shell @ root and root/test
func TestLoadGrimoireShape(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux")
	}
	ctl, err := New()
	if err != nil {
		t.Fatal(err)
	}
	name := "tp-test-grimoire"
	_ = ctl.Kill(name)
	defer func() { _ = ctl.Kill(name) }()

	root := "/tmp/tp-grimoire"
	testDir := root + "/test"
	_ = exec.Command("mkdir", "-p", testDir).Run()

	p := &store.Preset{
		Name: name,
		Cwd:  root,
		Windows: []store.PresetWindow{
			{
				Name: "editor",
				Cwd:  root,
				Panes: []store.PresetPane{
					{Idx: 1, Cwd: root, Cmd: "nvim"},
				},
			},
			{
				Name: "test",
				Cwd:  root,
				Panes: []store.PresetPane{
					{Idx: 1, Cwd: root, Cmd: ""},
					{Idx: 2, Cwd: testDir, Cmd: ""},
				},
			},
		},
	}
	if err := ctl.Load(p); err != nil {
		t.Fatal(err)
	}
	if !ctl.Has(name) {
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

	// only this session — no -a
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
	if testing.Short() {
		t.Skip("live tmux")
	}
	ctl, err := New()
	if err != nil {
		t.Fatal(err)
	}
	name := "tp-test-kho"
	_ = ctl.Kill(name)
	defer func() { _ = ctl.Kill(name) }()

	root := "/tmp/tp-kho"
	a, b := root+"/cong-dlqg", root+"/kho-dl-mo"
	_ = exec.Command("mkdir", "-p", a, b).Run()

	p := &store.Preset{
		Name: name,
		Cwd:  root,
		Windows: []store.PresetWindow{
			{Name: "kho-cong", Cwd: root, Panes: []store.PresetPane{{Cwd: root, Cmd: "nvim"}}},
			{Name: "shell", Cwd: root, Panes: []store.PresetPane{{Cwd: a}, {Cwd: b}}},
			{Name: "files", Cwd: root, Panes: []store.PresetPane{{Cwd: root, Cmd: "yazi"}}},
		},
	}
	if err := ctl.Load(p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	out, err := exec.Command("tmux", "list-windows", "-t", name, "-F", "#{window_name}:#{window_panes}").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(out))
	t.Log("windows:\n" + got)
	want := []string{"kho-cong:1", "shell:2", "files:1"}
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
		if win == "kho-cong" && (tool == "nvim" || start == "nvim") {
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
