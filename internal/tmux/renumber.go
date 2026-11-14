package tmux

import (
	"context"
	"fmt"
	"strings"
)

const reconcileRenumberMarker = "@gotomux_reconcile_renumber_v1"

// StabilizeWindowIndices disables renumbering for one journaled reconcile run.
// The original local/global setting is stored on the session before it is
// changed, so another process can restore it after a crash or cancellation.
func (c *Ctl) StabilizeWindowIndices(ctx context.Context, sessionID, operationID string) (func() error, error) {
	if !strings.HasPrefix(sessionID, "$") || operationID == "" || strings.Contains(operationID, "|") {
		return nil, fmt.Errorf("stable-index mode requires a session ID and valid operation ID")
	}
	marker, err := tmuxCmd(ctx, "show-options", "-q", "-v", "-t", sessionID, reconcileRenumberMarker)
	if err != nil {
		return nil, fmt.Errorf("read reconcile renumber marker: %w", err)
	}
	origin, original := "", ""
	if marker != "" {
		parts := strings.Split(marker, "|")
		if len(parts) != 3 || parts[0] != operationID {
			return nil, fmt.Errorf("session has an unresolved renumber marker from another operation")
		}
		origin, original = parts[1], parts[2]
	} else {
		local, err := tmuxCmd(ctx, "show-options", "-q", "-v", "-t", sessionID, "renumber-windows")
		if err != nil {
			return nil, fmt.Errorf("read local renumber-windows option: %w", err)
		}
		if local != "" {
			origin, original = "local", strings.TrimSpace(local)
		} else {
			global, err := tmuxCmd(ctx, "show-options", "-g", "-v", "renumber-windows")
			if err != nil {
				return nil, fmt.Errorf("read global renumber-windows option: %w", err)
			}
			origin, original = "global", strings.TrimSpace(global)
		}
		if original != "on" {
			return func() error { return nil }, nil
		}
		if err := tmuxRun(ctx, "set-option", "-t", sessionID, reconcileRenumberMarker, operationID+"|"+origin+"|"+original); err != nil {
			return nil, fmt.Errorf("record renumber-windows restore state: %w", err)
		}
	}
	if original == "on" {
		if err := tmuxRun(ctx, "set-option", "-t", sessionID, "renumber-windows", "off"); err != nil {
			_ = restoreRenumber(ctx, c, sessionID, operationID)
			return nil, fmt.Errorf("disable renumber-windows: %w", err)
		}
	}
	return func() error { return restoreRenumber(ctx, c, sessionID, operationID) }, nil
}

func restoreRenumber(ctx context.Context, ctl *Ctl, sessionID, operationID string) error {
	marker, err := tmuxCmd(ctx, "show-options", "-q", "-v", "-t", sessionID, reconcileRenumberMarker)
	if err != nil {
		return fmt.Errorf("read renumber restore marker: %w", err)
	}
	if marker == "" {
		return nil
	}
	parts := strings.Split(marker, "|")
	if len(parts) != 3 || parts[0] != operationID {
		return fmt.Errorf("renumber restore marker belongs to another operation")
	}
	switch parts[1] {
	case "local":
		if err := tmuxRun(ctx, "set-option", "-t", sessionID, "renumber-windows", parts[2]); err != nil {
			return fmt.Errorf("restore local renumber-windows: %w", err)
		}
	case "global":
		if err := tmuxRun(ctx, "set-option", "-u", "-t", sessionID, "renumber-windows"); err != nil {
			return fmt.Errorf("restore inherited renumber-windows: %w", err)
		}
	default:
		return fmt.Errorf("renumber restore marker has unknown scope %q", parts[1])
	}
	if err := tmuxRun(ctx, "set-option", "-u", "-t", sessionID, reconcileRenumberMarker); err != nil {
		return fmt.Errorf("clear renumber restore marker: %w", err)
	}
	return nil
}
