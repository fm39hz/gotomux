package reconcile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fm39hz/gotomux/internal/model"
)

func validatePlannedPaths(p Plan, sessionCwd string) error {
	for _, create := range p.Creates {
		if err := validateWindowPaths(create.Window, sessionCwd); err != nil {
			return fmt.Errorf("preflight window %d: %w", create.Index, err)
		}
	}
	for _, repair := range p.Repairs {
		for _, pane := range repair.MissingPanes {
			cwd := pane.Cwd
			if cwd == "" {
				cwd = repair.WindowCwd
			}
			if cwd == "" {
				cwd = sessionCwd
			}
			if err := validateDir(cwd); err != nil {
				return fmt.Errorf("preflight pane in window %d: %w", repair.Index, err)
			}
		}
	}
	return nil
}

func validateWindowPaths(w model.Window, sessionCwd string) error {
	if len(w.Panes) == 0 {
		return nil
	}
	for _, pane := range w.Panes {
		cwd := pane.Cwd
		if cwd == "" {
			cwd = w.Cwd
		}
		if cwd == "" {
			cwd = sessionCwd
		}
		if err := validateDir(cwd); err != nil {
			return err
		}
	}
	return nil
}

func validateDir(path string) error {
	if path == "" {
		path, _ = os.Getwd()
	}
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("working directory %q is unavailable: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", path)
	}
	return nil
}
