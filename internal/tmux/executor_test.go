package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTmuxCmdUsesExistingDefaultServerAndReturnsTrimmedOutput(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	envFile := filepath.Join(dir, "tmux-tmpdir")
	tmuxTmpDir := filepath.Join(dir, "sockets")
	writeFakeTmux(t, dir, `printf '%s\n' "$@" > "$GOTOMUX_CAPTURE_ARGS"
printf '%s\n' "$TMUX_TMPDIR" > "$GOTOMUX_CAPTURE_TMPDIR"
printf 'answer\n\n'
`)
	setFakeTmuxEnv(t, dir, argsFile, envFile, tmuxTmpDir)

	out, err := tmuxCmd(context.Background(), "display-message", "-p", "#{session_name}")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Fatalf("tmuxCmd output = %q, want %q", out, "answer")
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
	if !contains(argv, "-N") {
		t.Fatalf("tmux argv %q does not forbid daemon auto-start with -N", argv)
	}
	if !contains(argv, "-L") || !contains(argv, "default") {
		t.Fatalf("tmux argv %q does not select the default named socket", argv)
	}
	gotTmpDir, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(gotTmpDir)) != tmuxTmpDir {
		t.Fatalf("child TMUX_TMPDIR = %q, want %q", strings.TrimSpace(string(gotTmpDir)), tmuxTmpDir)
	}
}

func TestTmuxCmdCopiesStderrToExitErrorForNoServerDetection(t *testing.T) {
	dir := t.TempDir()
	writeFakeTmux(t, dir, `printf 'no server running on fake-socket\n' >&2
exit 1
`)
	setFakeTmuxEnv(t, dir, filepath.Join(dir, "args"), filepath.Join(dir, "tmpdir"), filepath.Join(dir, "sockets"))

	_, err := tmuxCmd(context.Background(), "list-sessions", "-F", "#{session_name}")
	if err == nil || !IsNoServerError(err) {
		t.Fatalf("tmuxCmd error = %v, IsNoServerError = false; want no-server error", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("tmuxCmd error %T does not expose *exec.ExitError", err)
	}
	if !strings.Contains(string(exitErr.Stderr), "no server running on fake-socket") {
		t.Fatalf("ExitError.Stderr = %q, want no-server diagnostic", exitErr.Stderr)
	}
}

func TestRunChainUsesExistingDefaultServer(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	writeFakeTmux(t, dir, `printf '%s\n' "$@" > "$GOTOMUX_CAPTURE_ARGS"
`)
	setFakeTmuxEnv(t, dir, argsFile, filepath.Join(dir, "tmpdir"), filepath.Join(dir, "sockets"))

	if err := tmuxRunChain(context.Background(),
		[]string{"set-option", "-g", "@example", "two words"},
		nil,
		[]string{"select-window", "-t", "$1:2"},
	); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
	if !contains(argv, "-N") {
		t.Fatalf("tmux chain argv %q does not forbid daemon auto-start with -N", argv)
	}
	if !contains(argv, ";") {
		t.Fatalf("tmux chain argv %q lost its command separator", argv)
	}
}

func TestSwitchObservedClientUsesExactRuntimeTarget(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	writeFakeTmux(t, dir, `printf '%s\n' "$@" > "$GOTOMUX_CAPTURE_ARGS"`)
	setFakeTmuxEnv(t, dir, argsFile, filepath.Join(dir, "tmpdir"), filepath.Join(dir, "sockets"))
	t.Setenv("TMUX", "/tmp/socket,1234,0")
	if err := switchObservedClient(context.Background(), "$19"); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(string(argv), "\n"); !strings.Contains(got, "switch-client") || !strings.Contains(got, "$19") {
		t.Fatalf("switch argv=%q", got)
	}
}

func TestTmuxCmdAllowsExplicitNewSessionToStartServer(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	writeFakeTmux(t, dir, `printf '%s\n' "$@" > "$GOTOMUX_CAPTURE_ARGS"
`)
	setFakeTmuxEnv(t, dir, argsFile, filepath.Join(dir, "tmpdir"), filepath.Join(dir, "sockets"))

	if _, err := tmuxCmd(context.Background(), "new-session", "-d", "-s", "created"); err != nil {
		t.Fatal(err)
	}
	assertHasArg(t, argsFile, "-N", false)
}

func TestTmuxRunAllowsExplicitStartServer(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	writeFakeTmux(t, dir, `printf '%s\n' "$@" > "$GOTOMUX_CAPTURE_ARGS"
`)
	setFakeTmuxEnv(t, dir, argsFile, filepath.Join(dir, "tmpdir"), filepath.Join(dir, "sockets"))

	if err := tmuxRun(context.Background(), "start-server"); err != nil {
		t.Fatal(err)
	}
	assertHasArg(t, argsFile, "-N", false)
}

func TestTmuxRunChainFirstCommandMayStartServer(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	writeFakeTmux(t, dir, `printf '%s\n' "$@" > "$GOTOMUX_CAPTURE_ARGS"
`)
	setFakeTmuxEnv(t, dir, argsFile, filepath.Join(dir, "tmpdir"), filepath.Join(dir, "sockets"))

	if err := tmuxRunChain(context.Background(), nil,
		[]string{"new-session", "-d", "-s", "created"},
		[]string{"set-option", "-g", "@example", "value"},
	); err != nil {
		t.Fatal(err)
	}
	assertHasArg(t, argsFile, "-N", false)
	assertHasArg(t, argsFile, ";", true)
}

func TestTmuxCmdUsesTMUXSocket(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	tmuxSocket := filepath.Join(dir, "selected-tmux-socket")
	writeFakeTmux(t, dir, `printf '%s\n' "$@" > "$GOTOMUX_CAPTURE_ARGS"
`)
	setFakeTmuxEnv(t, dir, argsFile, filepath.Join(dir, "tmpdir"), filepath.Join(dir, "sockets"))
	t.Setenv("TMUX", tmuxSocket+",12345,2")
	t.Setenv("TMUX_PANE", "%7")

	if _, err := tmuxCmd(context.Background(), "display-message", "-p", "#{session_name}"); err != nil {
		t.Fatal(err)
	}
	assertHasArg(t, argsFile, "-S", true)
	assertHasArg(t, argsFile, tmuxSocket, true)
	assertHasArg(t, argsFile, "-L", false)
}

func writeFakeTmux(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nset -eu\n" + body
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func setFakeTmuxEnv(t *testing.T, path, argsFile, tmpDirFile, tmuxTmpDir string) {
	t.Helper()
	t.Setenv("PATH", path+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	t.Setenv("GOTOMUX_CAPTURE_ARGS", argsFile)
	t.Setenv("GOTOMUX_CAPTURE_TMPDIR", tmpDirFile)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertHasArg(t *testing.T, argsFile, want string, has bool) {
	t.Helper()
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
	if got := contains(argv, want); got != has {
		t.Fatalf("tmux argv %q contains %q = %v, want %v", argv, want, got, has)
	}
}
