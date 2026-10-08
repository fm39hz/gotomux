package reconcile

import (
	"context"
	"fmt"
	"strings"
)

func disableRenumber(ctx context.Context, ops Executor, session string) (func() error, error) {
	value, err := ops.ShowOption(ctx, session, "renumber-windows")
	if err != nil {
		return nil, fmt.Errorf("renumber-windows: %w", err)
	}
	local := strings.TrimSpace(value) != ""
	if !local {
		value, err = ops.ShowOption(ctx, "", "renumber-windows")
		if err != nil {
			return nil, fmt.Errorf("renumber-windows (global): %w", err)
		}
	}
	if strings.TrimSpace(value) != "on" {
		return func() error { return nil }, nil
	}
	if err := ops.SetOption(ctx, session, "renumber-windows", "off"); err != nil {
		return nil, fmt.Errorf("renumber-windows off: %w", err)
	}
	return func() error {
		if local {
			return ops.SetOption(ctx, session, "renumber-windows", "on")
		}
		return ops.UnsetOption(ctx, session, "renumber-windows")
	}, nil
}

func combineRestoreError(operationErr, restoreErr error) error {
	if restoreErr == nil {
		return operationErr
	}
	if operationErr == nil {
		return fmt.Errorf("restore renumber-windows option: %w", restoreErr)
	}
	return fmt.Errorf("%w (also failed to restore renumber-windows option: %v)", operationErr, restoreErr)
}
