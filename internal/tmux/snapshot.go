package tmux

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
)

const (
	sessionIdentityFormat  = "#{session_name}\t#{session_id}\t#{@gotomux_session_id_v1}\t#{@gotomux_session_op_v1}"
	windowMembershipFormat = "#{session_id}\t#{window_id}\t#{window_index}\t#{@gotomux_window_slot_v1}\t#{@gotomux_window_op_v1}\t#{window_layout}\t#{window_panes}"
	paneIdentityFormat     = "#{window_id}\t#{pane_id}\t#{@gotomux_pane_slot_v1}\t#{@gotomux_pane_op_v1}"
)

// Observe returns detailed target-session state plus server-wide object
// identity and membership. All queries are bracketed by the same server
// generation, so a restart or target replacement cannot yield a partial view.
func (c *Ctl) Observe(ctx context.Context, targetName string) (*model.Snapshot, error) {
	target, err := c.Freeze(ctx, targetName)
	if err != nil {
		return nil, err
	}
	if target.ServerKey == "" || target.TmuxID == "" {
		return nil, fmt.Errorf("target session observation lacks runtime identity")
	}
	sessionRows, err := tmuxCmd(ctx, "list-sessions", "-F", sessionIdentityFormat)
	if err != nil {
		return nil, fmt.Errorf("list session identities: %w", err)
	}
	windowRows, err := tmuxCmd(ctx, "list-windows", "-a", "-F", windowMembershipFormat)
	if err != nil {
		return nil, fmt.Errorf("list window memberships: %w", err)
	}
	paneRows, err := tmuxCmd(ctx, "list-panes", "-a", "-F", paneIdentityFormat)
	if err != nil {
		return nil, fmt.Errorf("list pane identities: %w", err)
	}
	snapshot, err := buildSnapshot(target, target.ServerKey, sessionRows, windowRows, paneRows)
	if err != nil {
		return nil, err
	}
	activeRows, err := tmuxCmd(ctx, "list-windows", "-t", "="+targetName, "-F", "#{?window_active,#{window_id},}")
	if err != nil {
		return nil, fmt.Errorf("observe active window: %w", err)
	}
	for _, line := range strings.Split(activeRows, "\n") {
		if strings.HasPrefix(line, "@") {
			snapshot.ActiveWindowID = line
			break
		}
	}
	if after := tmuxServerKey(ctx, targetName); after == "" || after != target.ServerKey {
		return nil, fmt.Errorf("tmux server changed while observing session %q; retry", targetName)
	}
	return snapshot, nil
}

// buildSnapshot combines detailed target-session state with server-wide
// identity and membership indexes. The inputs are tmux -F formatted rows.
func buildSnapshot(target *model.Session, serverKey, sessionRows, windowRows, paneRows string) (*model.Snapshot, error) {
	if target == nil || target.Name == "" || target.TmuxID == "" {
		return nil, fmt.Errorf("snapshot target has no session runtime identity")
	}
	if serverKey == "" {
		return nil, fmt.Errorf("snapshot has no tmux server generation")
	}
	sessions, err := parseSessionIdentityRows(sessionRows)
	if err != nil {
		return nil, err
	}
	targetRef, ok := sessions[target.TmuxID]
	if !ok || targetRef.Name != target.Name || (target.LogicalID != "" && targetRef.LogicalID != target.LogicalID) {
		return nil, fmt.Errorf("target session identity changed during observation")
	}
	windowIDs, memberships, err := parseWindowMembershipRows(windowRows, sessions)
	if err != nil {
		return nil, err
	}
	objects, err := parsePaneIdentityRows(paneRows, windowIDs)
	if err != nil {
		return nil, err
	}

	for _, w := range target.Windows {
		if w.TmuxID == "" {
			return nil, fmt.Errorf("target snapshot has a window without runtime identity")
		}
		membershipFound := false
		for _, membership := range memberships {
			if membership.SessionID == target.TmuxID && membership.WindowID == w.TmuxID && membership.Index == w.Idx {
				membershipFound = true
				break
			}
		}
		if !membershipFound {
			return nil, fmt.Errorf("target window %s membership changed during observation", w.TmuxID)
		}
		if !hasObject(objects, "window", w.TmuxID, w.SlotID) {
			return nil, fmt.Errorf("target window %s identity changed during observation", w.TmuxID)
		}
		for _, p := range w.Panes {
			if p.TmuxID == "" || !hasObject(objects, "pane", p.TmuxID, p.SlotID) {
				return nil, fmt.Errorf("target pane identity changed during observation in window %s", w.TmuxID)
			}
		}
	}

	snapshot := &model.Snapshot{
		ServerKey: serverKey, Complete: true, Target: model.ObserveSession(*target),
		TargetSessionID: target.TmuxID, Memberships: memberships,
	}
	for _, ref := range sessions {
		snapshot.Sessions = append(snapshot.Sessions, ref)
		snapshot.Objects = append(snapshot.Objects, model.ObjectIdentity{Kind: model.ObjectSession, RuntimeID: ref.RuntimeID, LogicalID: ref.LogicalID, OperationID: ref.OperationID})
	}
	snapshot.Objects = append(snapshot.Objects, objects...)
	sort.Slice(snapshot.Sessions, func(i, j int) bool { return snapshot.Sessions[i].RuntimeID < snapshot.Sessions[j].RuntimeID })
	sort.Slice(snapshot.Objects, func(i, j int) bool {
		if snapshot.Objects[i].Kind != snapshot.Objects[j].Kind {
			return snapshot.Objects[i].Kind < snapshot.Objects[j].Kind
		}
		return snapshot.Objects[i].RuntimeID < snapshot.Objects[j].RuntimeID
	})
	sort.Slice(snapshot.Memberships, func(i, j int) bool {
		if snapshot.Memberships[i].SessionID != snapshot.Memberships[j].SessionID {
			return snapshot.Memberships[i].SessionID < snapshot.Memberships[j].SessionID
		}
		return snapshot.Memberships[i].Index < snapshot.Memberships[j].Index
	})
	return snapshot, nil
}

func parseSessionIdentityRows(raw string) (map[string]model.SessionIdentity, error) {
	refs := make(map[string]model.SessionIdentity)
	seenNames := make(map[string]string)
	rows, err := formattedRows(raw, 4)
	if err != nil {
		return nil, err
	}
	for _, fields := range rows {
		ref := model.SessionIdentity{Name: fields[0], RuntimeID: fields[1], LogicalID: fields[2], OperationID: fields[3]}
		if ref.Name == "" || ref.RuntimeID == "" {
			return nil, fmt.Errorf("incomplete session identity row")
		}
		if _, exists := refs[ref.RuntimeID]; exists {
			return nil, fmt.Errorf("duplicate session runtime identity %s", ref.RuntimeID)
		}
		if prior, exists := seenNames[ref.Name]; exists {
			return nil, fmt.Errorf("duplicate session name %q for %s and %s", ref.Name, prior, ref.RuntimeID)
		}
		refs[ref.RuntimeID] = ref
		seenNames[ref.Name] = ref.RuntimeID
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("empty session identity snapshot")
	}
	return refs, nil
}

func parseWindowMembershipRows(raw string, sessions map[string]model.SessionIdentity) (map[string]model.ObjectIdentity, []model.WindowMembership, error) {
	windowIDs := make(map[string]model.ObjectIdentity)
	seen := make(map[string]bool)
	indexOwner := make(map[string]string)
	var memberships []model.WindowMembership
	rows, err := formattedRows(raw, 7)
	if err != nil {
		return nil, nil, err
	}
	for _, fields := range rows {
		sessionID, windowID, logicalID, operationID := fields[0], fields[1], fields[3], fields[4]
		if _, ok := sessions[sessionID]; !ok {
			return nil, nil, fmt.Errorf("window membership references unknown session %s", sessionID)
		}
		if windowID == "" {
			return nil, nil, fmt.Errorf("window membership has no runtime identity")
		}
		index, err := strconv.Atoi(fields[2])
		if err != nil || index < 0 {
			return nil, nil, fmt.Errorf("window %s has invalid index %q", windowID, fields[2])
		}
		paneCount, err := strconv.Atoi(fields[6])
		if err != nil || paneCount < 1 {
			return nil, nil, fmt.Errorf("window %s has invalid pane count %q", windowID, fields[6])
		}
		key := sessionID + "\x00" + windowID
		if seen[key] {
			return nil, nil, fmt.Errorf("duplicate membership for window %s in session %s", windowID, sessionID)
		}
		seen[key] = true
		indexKey := sessionID + "\x00" + strconv.Itoa(index)
		if prior, exists := indexOwner[indexKey]; exists && prior != windowID {
			return nil, nil, fmt.Errorf("duplicate window index %d in session %s", index, sessionID)
		}
		indexOwner[indexKey] = windowID
		if prior, exists := windowIDs[windowID]; exists && (prior.SlotID != logicalID || prior.OperationID != operationID || prior.Layout != fields[5] || prior.PaneCount != paneCount) {
			return nil, nil, fmt.Errorf("window %s has conflicting slot identities", windowID)
		}
		windowIDs[windowID] = model.ObjectIdentity{Kind: model.ObjectWindow, RuntimeID: windowID, SlotID: logicalID, OperationID: operationID, Layout: fields[5], PaneCount: paneCount}
		memberships = append(memberships, model.WindowMembership{SessionID: sessionID, WindowID: windowID, Index: index})
	}
	if len(memberships) == 0 {
		return nil, nil, fmt.Errorf("empty window membership snapshot")
	}
	return windowIDs, memberships, nil
}

func parsePaneIdentityRows(raw string, windowIDs map[string]model.ObjectIdentity) ([]model.ObjectIdentity, error) {
	byPane := make(map[string]model.ObjectIdentity)
	rows, err := formattedRows(raw, 4)
	if err != nil {
		return nil, err
	}
	for _, fields := range rows {
		windowID, paneID, logicalID, operationID := fields[0], fields[1], fields[2], fields[3]
		if _, ok := windowIDs[windowID]; !ok {
			return nil, fmt.Errorf("pane %s references unknown window %s", paneID, windowID)
		}
		if paneID == "" {
			return nil, fmt.Errorf("pane identity row has no runtime ID")
		}
		object := model.ObjectIdentity{Kind: model.ObjectPane, RuntimeID: paneID, SlotID: logicalID, OperationID: operationID, ParentID: windowID}
		if prior, exists := byPane[paneID]; exists {
			if prior.ParentID != windowID || prior.SlotID != logicalID || prior.OperationID != operationID {
				return nil, fmt.Errorf("pane %s has conflicting identity rows", paneID)
			}
			continue // linked window: the same pane is listed once per membership
		}
		byPane[paneID] = object
	}
	if len(byPane) == 0 {
		return nil, fmt.Errorf("empty pane identity snapshot")
	}
	objects := make([]model.ObjectIdentity, 0, len(windowIDs)+len(byPane))
	for _, window := range windowIDs {
		objects = append(objects, window)
	}
	for _, object := range byPane {
		objects = append(objects, object)
	}
	return objects, nil
}

func formattedRows(raw string, fields int) ([][]string, error) {
	trimmed := strings.TrimRight(raw, "\n")
	if trimmed == "" {
		return nil, nil
	}
	var rows [][]string
	for lineNumber, line := range strings.Split(trimmed, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != fields {
			return nil, fmt.Errorf("tmux snapshot row %d has %d fields; expected %d", lineNumber+1, len(parts), fields)
		}
		rows = append(rows, parts)
	}
	return rows, nil
}

func hasObject(objects []model.ObjectIdentity, kind model.ObjectKind, runtimeID, stableID string) bool {
	for _, object := range objects {
		if object.Kind == kind && object.RuntimeID == runtimeID {
			if kind == model.ObjectSession {
				return object.LogicalID == stableID
			}
			return object.SlotID == stableID
		}
	}
	return false
}
