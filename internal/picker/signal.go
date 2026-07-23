package picker

import (
	"context"
	"errors"
	"os"
	"os/signal"

	tea "charm.land/bubbletea/v2"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// HoldInterrupt discards SIGINT during critical sections (freeze/save tx).
// Call stop() when the section ends to restore default SIGINT disposition.
func HoldInterrupt() (stop func()) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	go func() {
		<-ctx.Done()
	}()
	return cancel
}

// ErrCancel is returned when the user cancels the picker (Esc/Ctrl+C).
// Callers treat it as a clean exit, not a failure.
var ErrCancel = errors.New("canceled")

// RunPicker runs the interactive picker in a loop. On connect error, it
// re-shows the picker with the error message in the status line.
func RunPicker(cfg *config.Config, ctl tmux.Connector, st store.Storer, createName, createCwd string, connect func(Item) error) error {
	var lastErr string
	for {
		m := NewModel(cfg, ctl, st, createName, createCwd)
		m.ui.status = lastErr
		lastErr = ""

		opts, _, err := TeaOpts()
		if err != nil {
			return err
		}

		p := tea.NewProgram(m, opts...)
		final, runErr := RunCancellable(p)
		if runErr != nil {
			return runErr
		}

		fm, ok := final.(interface{ Done() Result })
		if !ok {
			return ErrCancel
		}

		res := fm.Done()
		switch res.Action {
		case ActionConnect:
			if err := connect(res.Item); err != nil {
				lastErr = err.Error()
				continue
			}
			return nil
		default:
			return ErrCancel
		}
	}
}

// RunCancellable wraps a Bubble Tea program with SIGINT handling.
// SIGINT triggers tea.Quit, returning the current model.
// Returns tea.Model even on interrupt so caller can read Done().
func RunCancellable(p *tea.Program) (tea.Model, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		<-ctx.Done()
		p.Quit()
	}()

	final, err := p.Run()
	if err != nil && err != tea.ErrInterrupted {
		return final, err
	}
	return final, nil
}
