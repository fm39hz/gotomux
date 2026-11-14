package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
)

type IdentityKind = model.ObjectKind

const (
	IdentitySession = model.ObjectSession
	IdentityWindow  = model.ObjectWindow
	IdentityPane    = model.ObjectPane
)

func identityOption(kind IdentityKind) (scopeFlag, option, idPrefix string, err error) {
	switch kind {
	case IdentitySession:
		return "", "@gotomux_session_id_v1", "$", nil
	case IdentityWindow:
		return "-w", "@gotomux_window_slot_v1", "@", nil
	case IdentityPane:
		return "-p", "@gotomux_pane_slot_v1", "%", nil
	default:
		return "", "", "", fmt.Errorf("unknown tmux identity kind %q", kind)
	}
}

func validateIdentityTarget(kind IdentityKind, runtimeID string) error {
	_, _, prefix, err := identityOption(kind)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(runtimeID, prefix) || len(runtimeID) == len(prefix) {
		return fmt.Errorf("%s identity requires a tmux runtime ID beginning with %q", kind, prefix)
	}
	if value, err := strconv.Atoi(runtimeID[1:]); err != nil || value < 0 || strconv.Itoa(value) != runtimeID[1:] {
		return fmt.Errorf("%s identity has malformed tmux runtime ID %q", kind, runtimeID)
	}
	return nil
}

// GetStableID reads session identity or a baseline slot ID from one exact
// tmux object addressed by its runtime ID.
func (c *Ctl) GetStableID(ctx context.Context, kind IdentityKind, runtimeID string) (string, error) {
	flag, option, _, err := identityOption(kind)
	if err != nil {
		return "", err
	}
	if err := validateIdentityTarget(kind, runtimeID); err != nil {
		return "", err
	}
	args := []string{"show-options", "-q", "-v"}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, "-t", runtimeID, option)
	value, err := tmuxCmd(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("read %s logical ID: %w", kind, err)
	}
	return strings.TrimSpace(value), nil
}

// SetStableID writes a versioned session identity or baseline slot ID to one
// exact tmux object.
func (c *Ctl) SetStableID(ctx context.Context, kind IdentityKind, runtimeID, logicalID string) error {
	flag, option, _, err := identityOption(kind)
	if err != nil {
		return err
	}
	if err := validateIdentityTarget(kind, runtimeID); err != nil {
		return err
	}
	if strings.TrimSpace(logicalID) == "" {
		return fmt.Errorf("%s logical ID cannot be empty", kind)
	}
	if err := identity.Validate(logicalID); err != nil {
		return fmt.Errorf("invalid logical ID: %w", err)
	}
	args := []string{"set-option"}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, "-t", runtimeID, option, logicalID)
	return tmuxRun(ctx, args...)
}

func (c *Ctl) UnsetStableID(ctx context.Context, kind IdentityKind, runtimeID string) error {
	flag, option, _, err := identityOption(kind)
	if err != nil {
		return err
	}
	if err := validateIdentityTarget(kind, runtimeID); err != nil {
		return err
	}
	args := []string{"set-option", "-u"}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, "-t", runtimeID, option)
	return tmuxRun(ctx, args...)
}

func operationOption(kind IdentityKind) (scopeFlag, option string, err error) {
	switch kind {
	case IdentitySession:
		return "", "@gotomux_session_op_v1", nil
	case IdentityWindow:
		return "-w", "@gotomux_window_op_v1", nil
	case IdentityPane:
		return "-p", "@gotomux_pane_op_v1", nil
	default:
		return "", "", fmt.Errorf("unknown tmux operation marker kind %q", kind)
	}
}

// SetOperationID marks a newly created tmux object so a retry can find it even
// if the process stopped between the tmux mutation and journal advancement.
func (c *Ctl) SetOperationID(ctx context.Context, kind IdentityKind, runtimeID, operationID string) error {
	flag, option, err := operationOption(kind)
	if err != nil {
		return err
	}
	if err := validateIdentityTarget(kind, runtimeID); err != nil {
		return err
	}
	if operationID == "" || len(operationID) > 160 || strings.ContainsAny(operationID, "\t\r\n") {
		return fmt.Errorf("invalid reconcile operation ID")
	}
	args := []string{"set-option"}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, "-t", runtimeID, option, operationID)
	return tmuxRun(ctx, args...)
}

func (c *Ctl) GetOperationID(ctx context.Context, kind IdentityKind, runtimeID string) (string, error) {
	flag, option, err := operationOption(kind)
	if err != nil {
		return "", err
	}
	if err := validateIdentityTarget(kind, runtimeID); err != nil {
		return "", err
	}
	args := []string{"show-options", "-q", "-v"}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, "-t", runtimeID, option)
	value, err := tmuxCmd(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func (c *Ctl) ClearOperationID(ctx context.Context, kind IdentityKind, runtimeID string) error {
	flag, option, err := operationOption(kind)
	if err != nil {
		return err
	}
	if err := validateIdentityTarget(kind, runtimeID); err != nil {
		return err
	}
	current, err := c.GetOperationID(ctx, kind, runtimeID)
	if err != nil || current == "" {
		return err
	}
	args := []string{"set-option", "-u"}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, "-t", runtimeID, option)
	return tmuxRun(ctx, args...)
}
