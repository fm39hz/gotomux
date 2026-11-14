package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
)

func (c *Ctl) CreateWindowWithOperation(ctx context.Context, sessionID string, idx int, want model.Window, sessionCwd, operationID string) (string, error) {
	return c.createWindowWithOperation(ctx, sessionID, idx, want, sessionCwd, operationID, false)
}

// CreateWindowWithOperationKeepingMarkers leaves operation evidence on every
// created object until the caller verifies and explicitly clears it. Exact
// rebuild uses this to distinguish its own partial output from copied slot tags.
func (c *Ctl) CreateWindowWithOperationKeepingMarkers(ctx context.Context, sessionID string, idx int, want model.Window, sessionCwd, operationID string) (string, error) {
	return c.createWindowWithOperation(ctx, sessionID, idx, want, sessionCwd, operationID, true)
}

func (c *Ctl) createWindowWithOperation(ctx context.Context, sessionID string, idx int, want model.Window, sessionCwd, operationID string, keepMarkers bool) (string, error) {
	if !strings.HasPrefix(sessionID, "$") || operationID == "" {
		return "", fmt.Errorf("create window: session runtime ID and operation ID are required")
	}
	if len(want.Panes) == 0 {
		want.Panes = []model.Pane{{Cwd: want.Cwd}}
	}
	sessionName, err := c.sessionNameByID(ctx, sessionID)
	if err != nil {
		return "", err
	}
	snapshot, err := c.Observe(ctx, sessionName)
	if err != nil {
		return "", err
	}
	if snapshot.TargetSessionID != sessionID {
		return "", fmt.Errorf("create window: target session identity changed")
	}
	windowID := findWindowForOperation(snapshot, want.SlotID, operationID, operationWindowName(operationID))
	if windowID == "" {
		if occupied(snapshot.Target.Windows, idx, "") {
			return "", fmt.Errorf("create window: target index %d is occupied", idx)
		}
		first := want.Panes[0]
		cwd := first.Cwd
		if cwd == "" {
			cwd = want.Cwd
		}
		if cwd == "" {
			cwd = sessionCwd
		}
		args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", windowTarget(sessionID, idx), "-n", operationWindowName(operationID), "-c", cwd}
		if cmd := paneCmd(first); cmd != "" {
			args = append(args, cmdArgs(cmd)...)
		}
		windowID, err = tmuxCmd(ctx, args...)
		if err != nil {
			return "", fmt.Errorf("create marked window: %w", err)
		}
		windowID = strings.TrimSpace(windowID)
		if !strings.HasPrefix(windowID, "@") {
			return "", fmt.Errorf("create marked window returned invalid ID %q", windowID)
		}
		if err := tmuxRun(ctx, "set-option", "-w", "-t", windowID, "automatic-rename", "off"); err != nil {
			return "", fmt.Errorf("disable automatic rename on new window %s: %w", windowID, err)
		}
		if err := c.SetOperationID(ctx, IdentityWindow, windowID, operationID); err != nil {
			return "", fmt.Errorf("mark new window %s: %w", windowID, err)
		}
		paneID, err := c.firstPaneID(ctx, windowID)
		if err != nil {
			return "", err
		}
		if err := c.SetOperationID(ctx, IdentityPane, paneID, paneOperationID(operationID, 0)); err != nil {
			return "", fmt.Errorf("mark initial pane %s: %w", paneID, err)
		}
		if err := c.ensureStableID(ctx, IdentityPane, paneID, first.SlotID); err != nil {
			return "", err
		}
	}
	if err := c.ensureWindowMembership(ctx, sessionID, sessionName, windowID, idx); err != nil {
		return "", err
	}
	if err := tmuxRun(ctx, "set-option", "-w", "-t", windowID, "automatic-rename", "off"); err != nil {
		return "", err
	}
	if err := c.ensureStableID(ctx, IdentityWindow, windowID, want.SlotID); err != nil {
		return "", err
	}
	if err := c.ensureOperationID(ctx, IdentityWindow, windowID, operationID); err != nil {
		return "", err
	}
	for paneIndex := range want.Panes {
		current, err := c.Observe(ctx, sessionName)
		if err != nil {
			return "", err
		}
		liveWindow, ok := findDetailedWindow(current, windowID)
		if !ok {
			return "", fmt.Errorf("created window %s disappeared while rebuilding panes", windowID)
		}
		wantPane := want.Panes[paneIndex]
		paneID := findPaneBySlotID(liveWindow, wantPane.SlotID)
		if paneID == "" {
			paneID = findObjectByOperation(current.Objects, IdentityPane, paneOperationID(operationID, paneIndex), windowID)
		}
		if paneID == "" && paneIndex == 0 && len(liveWindow.Panes) == 1 {
			paneID = liveWindow.Panes[0].TmuxID
		}
		if paneID == "" {
			expectedIDs := make([]string, 0, len(liveWindow.Panes))
			expectedPaneSlotIDs := make([]string, 0, len(liveWindow.Panes))
			for _, pane := range liveWindow.Panes {
				expectedIDs = append(expectedIDs, pane.TmuxID)
				if pane.SlotID != "" {
					expectedPaneSlotIDs = append(expectedPaneSlotIDs, pane.SlotID)
				}
			}
			paneID, err = c.AddPaneWithOperation(ctx, sessionID, windowID, len(liveWindow.Panes), expectedIDs, expectedPaneSlotIDs, wantPane, paneOperationID(operationID, paneIndex))
			if err != nil {
				return "", fmt.Errorf("create pane %d in window %s: %w", paneIndex, windowID, err)
			}
		}
		if err := c.ensureStableID(ctx, IdentityPane, paneID, wantPane.SlotID); err != nil {
			return "", err
		}
		if keepMarkers {
			if err := c.ensureOperationID(ctx, IdentityPane, paneID, paneOperationID(operationID, paneIndex)); err != nil {
				return "", err
			}
		} else {
			if err := c.ClearOperationID(ctx, IdentityPane, paneID); err != nil {
				return "", fmt.Errorf("clear pane operation marker %s: %w", paneID, err)
			}
		}
	}
	if want.Layout != "" {
		if err := c.ApplyLayoutAt(ctx, sessionID, windowID, idx, want.Layout); err != nil {
			return "", fmt.Errorf("apply created window layout: %w", err)
		}
	}
	name := safeWindowName(want.Name, sessionName)
	if name == "" {
		name = want.Role
	}
	if name == "" {
		name = "shell"
	}
	if err := c.RenameWindowAt(ctx, sessionID, windowID, idx, name); err != nil {
		return "", err
	}
	if !keepMarkers {
		if err := c.ClearOperationID(ctx, IdentityWindow, windowID); err != nil {
			return "", fmt.Errorf("clear window operation marker %s: %w", windowID, err)
		}
	}
	verified, err := c.Observe(ctx, sessionName)
	if err != nil {
		return "", fmt.Errorf("verify created window %s: %w", windowID, err)
	}
	if _, ok := findDetailedWindow(verified, windowID); !ok {
		return "", fmt.Errorf("created window %s is not a member of target session %s after construction", windowID, sessionID)
	}
	if keepMarkers && !operationMarkerForObject(verified.Objects, IdentityWindow, windowID, operationID) {
		return "", fmt.Errorf("created window %s lost operation marker %q during construction", windowID, operationID)
	}
	return windowID, nil
}

func operationMarkerForObject(objects []model.ObjectIdentity, kind IdentityKind, runtimeID, operationID string) bool {
	for _, object := range objects {
		if object.Kind == kind && object.RuntimeID == runtimeID {
			return object.OperationID == operationID
		}
	}
	return false
}

func (c *Ctl) AddPaneWithOperation(ctx context.Context, sessionID, windowID string, expectedCount int, expectedPaneIDs, expectedPaneSlotIDs []string, want model.Pane, operationID string) (string, error) {
	if !strings.HasPrefix(sessionID, "$") || !strings.HasPrefix(windowID, "@") || operationID == "" {
		return "", fmt.Errorf("add pane: session/window runtime IDs and operation ID are required")
	}
	sessionName, err := c.sessionNameByID(ctx, sessionID)
	if err != nil {
		return "", err
	}
	snapshot, err := c.Observe(ctx, sessionName)
	if err != nil {
		return "", err
	}
	window, ok := findDetailedWindow(snapshot, windowID)
	if !ok {
		return "", fmt.Errorf("add pane: target window %s is not in target session", windowID)
	}
	if id := findPaneBySlotID(window, want.SlotID); id != "" {
		return id, nil
	}
	if id := findObjectByOperation(snapshot.Objects, IdentityPane, operationID, windowID); id != "" {
		if err := c.ensureStableID(ctx, IdentityPane, id, want.SlotID); err != nil {
			return "", err
		}
		if err := c.ClearOperationID(ctx, IdentityPane, id); err != nil {
			return "", err
		}
		return id, nil
	}
	for _, expectedID := range expectedPaneIDs {
		if !hasPaneRuntimeID(window, expectedID) {
			return "", fmt.Errorf("add pane recovery: expected pane %s disappeared", expectedID)
		}
	}
	for _, expectedID := range expectedPaneSlotIDs {
		if findPaneBySlotID(window, expectedID) == "" {
			return "", fmt.Errorf("add pane recovery: expected pane slot %s disappeared", expectedID)
		}
	}
	if len(window.Panes) == expectedCount+1 {
		var candidate []string
		for _, pane := range window.Panes {
			if containsString(expectedPaneIDs, pane.TmuxID) || containsString(expectedPaneSlotIDs, pane.SlotID) {
				continue
			}
			if paneMatchesReplay(want, pane) {
				candidate = append(candidate, pane.TmuxID)
			}
		}
		if len(candidate) != 1 {
			return "", fmt.Errorf("add pane recovery: found %d plausible untagged panes; refusing to guess", len(candidate))
		}
		id := candidate[0]
		if err := c.SetOperationID(ctx, IdentityPane, id, operationID); err != nil {
			return "", err
		}
		if err := c.ensureStableID(ctx, IdentityPane, id, want.SlotID); err != nil {
			return "", err
		}
		if err := c.ClearOperationID(ctx, IdentityPane, id); err != nil {
			return "", err
		}
		return id, nil
	}
	if len(window.Panes) != expectedCount {
		return "", fmt.Errorf("add pane precondition: observed %d panes, expected %d", len(window.Panes), expectedCount)
	}
	args := []string{"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", windowID, "-h"}
	cwd := want.Cwd
	if cwd == "" {
		cwd = window.Cwd
	}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if cmd := paneCmd(want); cmd != "" {
		args = append(args, cmdArgs(cmd)...)
	}
	id, err := tmuxCmd(ctx, args...)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, "%") {
		return "", fmt.Errorf("split-window returned invalid pane ID %q", id)
	}
	if err := c.SetOperationID(ctx, IdentityPane, id, operationID); err != nil {
		return "", err
	}
	if err := c.ensureStableID(ctx, IdentityPane, id, want.SlotID); err != nil {
		return "", err
	}
	if err := c.ClearOperationID(ctx, IdentityPane, id); err != nil {
		return "", err
	}
	return id, nil
}

func (c *Ctl) SelectWindowByID(ctx context.Context, sessionID, windowID string) error {
	if !strings.HasPrefix(sessionID, "$") || !strings.HasPrefix(windowID, "@") {
		return fmt.Errorf("select window requires session and window runtime IDs")
	}
	return tmuxRun(ctx, "select-window", "-t", sessionID+":"+windowID)
}

func (c *Ctl) ensureWindowMembership(ctx context.Context, sessionID, sessionName, windowID string, idx int) error {
	snapshot, err := c.Observe(ctx, sessionName)
	if err != nil {
		return err
	}
	if snapshot.TargetSessionID != sessionID {
		return fmt.Errorf("window membership target session changed")
	}
	if member, ok := findMembership(snapshot.Memberships, sessionID, windowID); ok {
		if member.Index == idx {
			return nil
		}
		return c.MoveWindowTo(ctx, sessionID, windowID, member.Index, idx)
	}
	if occupied(snapshot.Target.Windows, idx, "") {
		return fmt.Errorf("window membership destination index %d is occupied", idx)
	}
	return c.LinkWindowTo(ctx, sessionID, windowID, idx)
}

func (c *Ctl) sessionNameByID(ctx context.Context, sessionID string) (string, error) {
	rows, err := tmuxCmd(ctx, "list-sessions", "-F", "#{session_id}\t#{session_name}")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(rows, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[0] == sessionID {
			return parts[1], nil
		}
	}
	return "", fmt.Errorf("session runtime ID %s is no longer present", sessionID)
}

func (c *Ctl) firstPaneID(ctx context.Context, windowID string) (string, error) {
	rows, err := tmuxCmd(ctx, "list-panes", "-t", windowID, "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}
	parts := strings.Split(rows, "\n")
	if len(parts) != 1 || !strings.HasPrefix(parts[0], "%") {
		return "", fmt.Errorf("new window %s has unexpected initial panes %q", windowID, rows)
	}
	return parts[0], nil
}

func (c *Ctl) ensureStableID(ctx context.Context, kind IdentityKind, runtimeID, stableID string) error {
	if stableID == "" {
		return nil
	}
	current, err := c.GetStableID(ctx, kind, runtimeID)
	if err != nil {
		return err
	}
	if current != "" && current != stableID {
		return fmt.Errorf("%s %s already belongs to stable ID %s", kind, runtimeID, current)
	}
	if current == "" {
		return c.SetStableID(ctx, kind, runtimeID, stableID)
	}
	return nil
}

func (c *Ctl) ensureOperationID(ctx context.Context, kind IdentityKind, runtimeID, operationID string) error {
	current, err := c.GetOperationID(ctx, kind, runtimeID)
	if err != nil {
		return fmt.Errorf("read %s operation marker: %w", kind, err)
	}
	if current != "" && current != operationID {
		return fmt.Errorf("%s %s carries operation marker %q; expected %q", kind, runtimeID, current, operationID)
	}
	if current == "" {
		return c.SetOperationID(ctx, kind, runtimeID, operationID)
	}
	return nil
}

func findWindowForOperation(snapshot *model.Snapshot, stableID, operationID, temporaryName string) string {
	if snapshot == nil {
		return ""
	}
	for _, object := range snapshot.Objects {
		if object.Kind != model.ObjectWindow {
			continue
		}
		if operationID != "" && object.OperationID == operationID {
			return object.RuntimeID
		}
		if stableID != "" && object.SlotID == stableID {
			return object.RuntimeID
		}
	}
	for _, window := range snapshot.Target.Windows {
		if window.Name == temporaryName {
			return window.TmuxID
		}
	}
	return ""
}

func findDetailedWindow(snapshot *model.Snapshot, id string) (model.Window, bool) {
	for _, window := range snapshot.Target.Windows {
		if window.TmuxID == id {
			return window, true
		}
	}
	return model.Window{}, false
}

func findObjectByOperation(objects []model.ObjectIdentity, kind IdentityKind, operationID, parentID string) string {
	for _, object := range objects {
		if object.Kind == kind && object.OperationID == operationID && object.ParentID == parentID {
			return object.RuntimeID
		}
	}
	return ""
}

func findPaneBySlotID(window model.Window, stableID string) string {
	if stableID == "" {
		return ""
	}
	for _, pane := range window.Panes {
		if pane.SlotID == stableID {
			return pane.TmuxID
		}
	}
	return ""
}

func hasPaneRuntimeID(window model.Window, id string) bool {
	for _, pane := range window.Panes {
		if pane.TmuxID == id {
			return true
		}
	}
	return false
}

func paneMatchesReplay(want, live model.Pane) bool {
	command := want.CmdPath
	if command == "" {
		fields := strings.Fields(want.StartCmd)
		if len(fields) > 0 {
			command = fields[0]
		}
	}
	if command == "" {
		command = want.Cmd
	}
	if command == "" {
		return false
	}
	commandMatches := command == live.CmdPath || filepath.Base(command) == filepath.Base(live.Cmd) || filepath.Base(command) == filepath.Base(live.StartCmd)
	return commandMatches && want.Cwd != "" && filepath.Clean(want.Cwd) == filepath.Clean(live.Cwd)
}

func paneOperationID(operationID string, paneIndex int) string {
	return fmt.Sprintf("%s/pane/%d", operationID, paneIndex)
}

func operationWindowName(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return "__gotomux_reconcile_" + hex.EncodeToString(sum[:12])
}

func findMembership(members []model.WindowMembership, sessionID, windowID string) (model.WindowMembership, bool) {
	for _, member := range members {
		if member.SessionID == sessionID && member.WindowID == windowID {
			return member, true
		}
	}
	return model.WindowMembership{}, false
}

func occupied(windows []model.Window, idx int, exceptID string) bool {
	for _, window := range windows {
		if window.Idx == idx && window.TmuxID != exceptID {
			return true
		}
	}
	return false
}

func containsString(haystack []string, value string) bool {
	for _, item := range haystack {
		if item == value {
			return true
		}
	}
	return false
}
