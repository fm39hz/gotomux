package picker

import (
	"errors"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// DrainSignals empties a signal channel so disposition stays clean after Stop.
func DrainSignals(ch <-chan os.Signal) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// HoldInterrupt discards SIGINT for a short critical section (freeze/save tx).
// Call stop() when the section ends - restores default disposition after drain.
func HoldInterrupt() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				// discard - ACID section must finish
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
		DrainSignals(ch)
	}
}

// ErrCancel is returned when the user cancels the picker (Esc/Ctrl+C).
// Callers treat it as a clean exit, not a failure.
var ErrCancel = errors.New("canceled")

// RunPicker runs the interactive picker in a loop. On connect error, it
// re-shows the picker with the error message in the status line.
func RunPicker(ctl tmux.Connector, st *store.Store, createName, createCwd string, connect func(Item) error) error {
	var lastErr string
	for {
		m := NewModel(ctl, st, createName, createCwd)
		m.status = lastErr
		lastErr = ""

		opts, alt, err := TeaOpts()
		if err != nil {
			return err
		}

		p := tea.NewProgram(m, opts...)
		final, runErr := RunCancellable(p)
		if runErr != nil {
			return runErr
		}

		fm, ok := final.(interface {
			Done() Result
			FrameLines() int
		})
		if !ok {
			return ErrCancel
		}

		if !alt {
			ClearInline(fm.FrameLines())
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

func RunCancellable(p *tea.Program) (tea.Model, error) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	sigDone := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			p.Quit()
		case <-sigDone:
		}
	}()
	final, err := p.Run()
	close(sigDone)
	signal.Stop(sigCh)
	DrainSignals(sigCh)
	if err != nil && err != tea.ErrInterrupted {
		return final, err
	}
	// interrupt/cancel -> model may be partial; caller reads Done()/name
	return final, nil
}
