package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	gotmux "github.com/zigai/gotmux/tmux"
)

// tmuxCmd executes one tmux command and returns stdout with trailing newlines
// removed, matching exec.Cmd.Output's existing callers.
func tmuxCmd(ctx context.Context, args ...string) (string, error) {
	result, err := executeTmuxCommand(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}

	return strings.TrimRight(string(result.Stdout), "\n"), nil
}

// tmuxRun executes one tmux command and includes captured output in failures,
// matching the diagnostic contract of exec.Cmd.CombinedOutput.
func tmuxRun(ctx context.Context, args ...string) error {
	result, err := executeTmuxCommand(ctx, args...)
	if err == nil {
		return nil
	}

	return fmt.Errorf("tmux %s: %w (%s)", strings.Join(args, " "), err, combinedOutput(result))
}

// tmuxRunChain executes tmux commands in one client process. The first
// non-empty command determines whether starting a missing server is allowed,
// matching tmux's new-session/start-server command behavior.
func tmuxRunChain(ctx context.Context, parts ...[]string) error {
	_, err := tmuxSequenceOutput(ctx, parts...)
	return err
}

func tmuxSequenceOutput(ctx context.Context, parts ...[]string) ([]byte, error) {
	commands := make([]gotmux.Command, 0, len(parts))
	start := gotmux.ExistingOnly

	for _, args := range parts {
		if len(args) == 0 {
			continue
		}

		command, err := gotmux.NewCommand(args[0], args[1:]...)
		if err != nil {
			return nil, fmt.Errorf("tmux chain: %w", err)
		}
		if len(commands) == 0 && commandStartsServer(args[0]) {
			start = gotmux.AllowStart
		}
		commands = append(commands, command)
	}

	if len(commands) == 0 {
		return nil, nil
	}

	sequence, err := gotmux.Sequence(commands...)
	if err != nil {
		return nil, fmt.Errorf("tmux chain: %w", err)
	}
	server, err := newTmuxServer()
	if err != nil {
		return nil, fmt.Errorf("tmux chain: %w", err)
	}
	result, err := server.RunSequenceWith(ctx, sequence, gotmux.RunOptions{Start: start})
	copyExitErrorStderr(err, result.Stderr)
	if err != nil {
		return result.Stdout, fmt.Errorf("tmux chain: %w (%s)", err, combinedOutput(result))
	}
	return result.Stdout, nil
}

func executeTmuxCommand(ctx context.Context, args ...string) (gotmux.Result, error) {
	if len(args) == 0 {
		return gotmux.Result{}, fmt.Errorf("tmux command is empty")
	}

	command, err := gotmux.NewCommand(args[0], args[1:]...)
	if err != nil {
		return gotmux.Result{}, fmt.Errorf("tmux command: %w", err)
	}
	server, err := newTmuxServer()
	if err != nil {
		return gotmux.Result{}, err
	}

	start := gotmux.ExistingOnly
	if commandStartsServer(args[0]) {
		start = gotmux.AllowStart
	}

	result, err := server.RunWith(ctx, command, gotmux.RunOptions{Start: start})
	copyExitErrorStderr(err, result.Stderr)
	return result, err
}

// New snapshots the current process environment, preserving tmux's normal
// TMUX socket selection and TMUX_TMPDIR named-socket resolution. Constructing
// per call also avoids freezing an earlier environment in a package singleton.
func newTmuxServer() (*gotmux.Server, error) {
	return gotmux.New(gotmux.Config{})
}

func commandStartsServer(name string) bool {
	return name == "new-session" || name == "start-server"
}

// gotmux retains subprocess stderr on CommandError.Result.Stderr, but
// exec.Cmd.Output callers inspect exec.ExitError.Stderr through errors.As.
// Copy the captured bytes onto the wrapped exit error so IsNoServerError keeps
// working without changing its existing implementation.
func copyExitErrorStderr(err error, stderr []byte) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitErr.Stderr = append([]byte(nil), stderr...)
	}
}

func combinedOutput(result gotmux.Result) string {
	output := append([]byte(nil), result.Stdout...)
	if len(output) > 0 && len(result.Stderr) > 0 && output[len(output)-1] != '\n' {
		output = append(output, '\n')
	}
	output = append(output, result.Stderr...)
	return strings.TrimSpace(string(output))
}
