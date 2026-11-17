package template

import (
	"context"
	"fmt"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// IdentityOps is the narrow port needed to observe and tag live tmux objects.
type IdentityOps interface {
	Observe(ctx context.Context, targetName string) (*model.Snapshot, error)
	SetStableID(ctx context.Context, kind tmux.IdentityKind, runtimeID, logicalID string) error
}

func EnsureLiveIdentities(ctx context.Context, ops IdentityOps, targetName string) (*model.Session, error) {
	return ensureLiveIdentities(ctx, ops, targetName, nil)
}

// EnsureLiveIdentitiesFromPreset preserves the persisted session and slot IDs
// while assigning them to the exact runtime objects created from that preset.
func EnsureLiveIdentitiesFromPreset(ctx context.Context, ops IdentityOps, targetName string, preferred *model.Session) (*model.Session, error) {
	return ensureLiveIdentities(ctx, ops, targetName, preferred)
}

func ensureLiveIdentities(ctx context.Context, ops IdentityOps, targetName string, preferred *model.Session) (*model.Session, error) {
	if ops == nil {
		return nil, fmt.Errorf("identity assignment: nil tmux identity port")
	}
	snapshot, err := ops.Observe(ctx, targetName)
	if err != nil {
		return nil, err
	}
	if err := validateIdentitySnapshot(snapshot, targetName); err != nil {
		return nil, err
	}
	if preferred != nil {
		if preferred.TmuxID == "" || preferred.TmuxID != snapshot.TargetSessionID || preferred.ServerKey == "" || preferred.ServerKey != snapshot.ServerKey {
			return nil, fmt.Errorf("loaded session incarnation changed before identity assignment")
		}
		if preferred.Name != targetName || len(preferred.Windows) != len(snapshot.Target.Windows) {
			return nil, fmt.Errorf("preset identity tree does not match loaded session topology")
		}
		seenWindows, seenPanes := map[string]bool{}, map[string]bool{}
		for wi := range preferred.Windows {
			pw := &preferred.Windows[wi]
			var lw *model.Window
			for li := range snapshot.Target.Windows {
				if snapshot.Target.Windows[li].TmuxID == pw.TmuxID {
					lw = &snapshot.Target.Windows[li]
					break
				}
			}
			if pw.TmuxID == "" || lw == nil || seenWindows[pw.TmuxID] {
				return nil, fmt.Errorf("creation receipt has missing or duplicate window handle %q", pw.TmuxID)
			}
			seenWindows[pw.TmuxID] = true
			if len(pw.Panes) != len(lw.Panes) {
				return nil, fmt.Errorf("preset identity tree window %d does not match loaded pane topology", wi)
			}
			for pi := range pw.Panes {
				pane := &pw.Panes[pi]
				found := false
				for _, lp := range lw.Panes {
					if lp.TmuxID == pane.TmuxID {
						found = true
						break
					}
				}
				if pane.TmuxID == "" || !found || seenPanes[pane.TmuxID] {
					return nil, fmt.Errorf("creation receipt has missing, moved, or duplicate pane handle %q", pane.TmuxID)
				}
				seenPanes[pane.TmuxID] = true
			}
		}
	}
	seen := logicalIDOwners(snapshot.Objects)
	assign := func(kind tmux.IdentityKind, runtimeID, current, preferredID string) error {
		if current != "" {
			if preferredID != "" && current != preferredID {
				return fmt.Errorf("loaded %s %s has logical ID %s, preset requires %s", kind, runtimeID, current, preferredID)
			}
			return nil
		}
		if preferredID != "" {
			if err := identity.Validate(preferredID); err != nil {
				return fmt.Errorf("preset contains malformed %s identity: %w", kind, err)
			}
			if owner, exists := seen[preferredID]; exists {
				return fmt.Errorf("preset %s identity %s is already owned by %s", kind, preferredID, owner)
			}
			if err := ops.SetStableID(ctx, kind, runtimeID, preferredID); err != nil {
				return fmt.Errorf("assign preset %s identity to %s: %w", kind, runtimeID, err)
			}
			seen[preferredID] = objectKey(kind, runtimeID)
			return nil
		}
		id, err := newUniqueID(seen)
		if err != nil {
			return err
		}
		if err := ops.SetStableID(ctx, kind, runtimeID, id); err != nil {
			return fmt.Errorf("assign %s logical ID to %s: %w", kind, runtimeID, err)
		}
		seen[id] = objectKey(kind, runtimeID)
		return nil
	}
	preferredSessionID := ""
	if preferred != nil {
		preferredSessionID = preferred.LogicalID
	}
	if err := assign(tmux.IdentitySession, snapshot.Target.TmuxID, snapshot.Target.LogicalID, preferredSessionID); err != nil {
		return nil, fmt.Errorf("identity assignment may be partial; rerun to resume: %w", err)
	}
	windowsToAssign := make([]struct {
		live   model.Window
		wanted *model.Window
	}, 0, len(snapshot.Target.Windows))
	if preferred == nil {
		for _, window := range snapshot.Target.Windows {
			windowsToAssign = append(windowsToAssign, struct {
				live   model.Window
				wanted *model.Window
			}{live: window})
		}
	} else {
		for wi := range preferred.Windows {
			wanted := &preferred.Windows[wi]
			for _, liveWindow := range snapshot.Target.Windows {
				if liveWindow.TmuxID == wanted.TmuxID {
					windowsToAssign = append(windowsToAssign, struct {
						live   model.Window
						wanted *model.Window
					}{live: liveWindow, wanted: wanted})
					break
				}
			}
		}
	}
	for _, target := range windowsToAssign {
		window := target.live
		preferredWindowID := ""
		if preferred != nil {
			preferredWindowID = target.wanted.SlotID
		}
		if err := assign(tmux.IdentityWindow, window.TmuxID, window.SlotID, preferredWindowID); err != nil {
			return nil, fmt.Errorf("identity assignment may be partial; rerun to resume: %w", err)
		}
		for _, pane := range window.Panes {
			preferredPaneID := ""
			if preferred != nil {
				for _, wanted := range target.wanted.Panes {
					if wanted.TmuxID == pane.TmuxID {
						preferredPaneID = wanted.SlotID
						break
					}
				}
			}
			if err := assign(tmux.IdentityPane, pane.TmuxID, pane.SlotID, preferredPaneID); err != nil {
				return nil, fmt.Errorf("identity assignment may be partial; rerun to resume: %w", err)
			}
		}
	}
	verified, err := ops.Observe(ctx, targetName)
	if err != nil {
		return nil, fmt.Errorf("identity tags were written but could not be verified; rerun to resume: %w", err)
	}
	if err := validateIdentitySnapshot(verified, targetName); err != nil {
		return nil, fmt.Errorf("identity tags were written but verification failed: %w", err)
	}
	if err := requireTargetIDs(verified.Target.Session()); err != nil {
		return nil, fmt.Errorf("identity tags were written but target remains incomplete: %w", err)
	}
	if verified.ServerKey != snapshot.ServerKey || verified.TargetSessionID != snapshot.TargetSessionID {
		return nil, fmt.Errorf("session incarnation changed during identity assignment")
	}
	verified.Target.SchemaVersion = model.CurrentBaselineSchemaVersion
	session := verified.Target.Session()
	return &session, nil
}

func validateIdentitySnapshot(snapshot *model.Snapshot, targetName string) error {
	if snapshot == nil || !snapshot.Complete || snapshot.ServerKey == "" {
		return fmt.Errorf("identity operation requires a complete server snapshot")
	}
	if snapshot.Target.Name != targetName || snapshot.Target.TmuxID == "" || snapshot.Target.TmuxID != snapshot.TargetSessionID || snapshot.Target.ServerKey != snapshot.ServerKey {
		return fmt.Errorf("identity operation target session or server identity mismatch")
	}
	owners := make(map[string]string)
	for _, object := range snapshot.Objects {
		if object.RuntimeID == "" || object.Kind == "" {
			return fmt.Errorf("snapshot contains an object without runtime identity")
		}
		stableID := object.SlotID
		if object.Kind == tmux.IdentitySession {
			stableID = object.LogicalID
		}
		if stableID == "" {
			continue
		}
		if err := identity.Validate(stableID); err != nil {
			return fmt.Errorf("snapshot contains malformed stable ID on %s %s: %w", object.Kind, object.RuntimeID, err)
		}
		if prior, exists := owners[stableID]; exists && prior != objectKey(object.Kind, object.RuntimeID) {
			return fmt.Errorf("stable ID %s is duplicated by %s and %s", stableID, prior, objectKey(object.Kind, object.RuntimeID))
		}
		owners[stableID] = objectKey(object.Kind, object.RuntimeID)
	}
	return nil
}

func requireTargetIDs(session model.Session) error {
	if session.LogicalID == "" {
		return fmt.Errorf("session %s has no logical ID", session.TmuxID)
	}
	for _, window := range session.Windows {
		if window.SlotID == "" {
			return fmt.Errorf("window %s has no logical ID", window.TmuxID)
		}
		for _, pane := range window.Panes {
			if pane.SlotID == "" {
				return fmt.Errorf("pane %s has no logical ID", pane.TmuxID)
			}
		}
	}
	return nil
}

func logicalIDOwners(objects []model.ObjectIdentity) map[string]string {
	owners := make(map[string]string)
	for _, object := range objects {
		stableID := object.SlotID
		if object.Kind == tmux.IdentitySession {
			stableID = object.LogicalID
		}
		if stableID != "" {
			owners[stableID] = objectKey(object.Kind, object.RuntimeID)
		}
	}
	return owners
}

func newUniqueID(seen map[string]string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := identity.New()
		if err != nil {
			return "", err
		}
		if _, exists := seen[id]; !exists {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique logical ID")
}

func objectKey(kind model.ObjectKind, runtimeID string) string { return string(kind) + " " + runtimeID }
