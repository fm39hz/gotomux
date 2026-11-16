package template

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/event"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
)

var globalBus *event.Bus

func SetEventBus(b *event.Bus) {
	globalBus = b
	if b != nil {
		b.On(event.FreezeDone, func(ctx context.Context, args ...any) {
			st := args[0].(store.Storer)
			shapeID := args[1].(string)
			p := args[2].(*model.Session)
			mirrorAfter(st, shapeID)
			observeAfterShape(st, shapeID, p)
		})
	}
}

func ReadSticky(st store.Storer) string {
	if st == nil {
		return "default"
	}
	id := st.StickyID()
	if id == "" {
		return "default"
	}
	return id
}

func StickyLabel(st store.Storer) string {
	if st == nil {
		return "default"
	}
	id := st.StickyID()
	if id == "" || id == "default" {
		return "default"
	}
	body, ok := st.GetShape(id)
	if !ok {
		return id
	}
	p, err := Parse(body)
	if err != nil {
		return id
	}
	p = ToShape(p, id)
	return ShapeLabel(p)
}

func LoadActive(st store.Storer) (*model.Session, string, error) {
	if st == nil {
		return builtinDefault(), "default", nil
	}
	ensureShapesReady(st)
	id := st.StickyID()
	if id == "" {
		id = "default"
	}
	body, ok := st.GetShape(id)
	if !ok {
		if err := ensureDefault(st); err != nil {
			return builtinDefault(), "default", fmt.Errorf("ensure default shape: %w", err)
		}
		return builtinDefault(), "default", nil
	}
	p, err := Parse(body)
	if err != nil {
		if err2 := ensureDefault(st); err2 != nil {
			return builtinDefault(), "default", fmt.Errorf("parse shape %q: %w (and ensure default: %v)", id, err, err2)
		}
		return builtinDefault(), "default", nil
	}
	return p, id, nil
}

func ensureDefault(st store.Storer) error {
	if st == nil {
		return fmt.Errorf("nil store")
	}
	def := builtinDefault()
	id, key, body := shapeBody(def, true)
	if err := st.UpsertShapeByID(id, key, body); err != nil {
		return err
	}
	writeConfigMirror(st, id, body)
	return nil
}

func emitShapeEvent(ctx context.Context, st store.Storer, shapeID string, p *model.Session) {
	if globalBus != nil {
		globalBus.Emit(ctx, event.FreezeDone, st, shapeID, p)
	} else {
		mirrorAfter(st, shapeID)
		observeAfterShape(st, shapeID, p)
	}
}

func StickFrom(st store.Storer, p *model.Session) (id string, created bool, err error) {
	if st == nil {
		return "", false, fmt.Errorf("stick: nil store")
	}
	if p == nil {
		return "", false, fmt.Errorf("stick: nil preset")
	}
	ensureShapesReady(st)
	id, key, body := shapeBody(p, false)
	outID, created, err := st.StickShape(id, key, body)
	if err != nil {
		return "", false, fmt.Errorf("stick shape: %w", err)
	}
	emitShapeEvent(context.Background(), st, outID, p)
	return outID, created, nil
}

func RememberShape(st store.Storer, p *model.Session) (id string, created bool, err error) {
	if st == nil || p == nil {
		return "", false, nil
	}
	ensureShapesReady(st)
	id, key, body := shapeBody(p, false)
	outID, created, err := st.RememberShapeOnly(id, key, body)
	if err != nil {
		return "", false, fmt.Errorf("remember shape: %w", err)
	}
	emitShapeEvent(context.Background(), st, outID, p)
	return outID, created, nil
}

func FreezeSave(st store.Storer, s *model.Session, setSticky bool) (shapeID string, shapeCreated bool, err error) {
	if st == nil || s == nil {
		return "", false, fmt.Errorf("freeze save: nil store or preset")
	}
	ensureShapesReady(st)
	baseline := cloneSession(s)
	baseline.SchemaVersion = model.CurrentBaselineSchemaVersion
	stampSemanticMetadata(baseline)
	id, key, body := shapeBody(s, false)
	shapeID, shapeCreated, err = st.SaveFreezeWithBaseline(s, baseline, id, key, body, setSticky)
	if err != nil {
		return "", false, fmt.Errorf("freeze save: %w", err)
	}
	emitShapeEvent(context.Background(), st, shapeID, s)
	return shapeID, shapeCreated, nil
}

func FreezeRemember(ctl tmux.Connector, st store.Storer, name string) (shapeID string, shapeCreated bool, err error) {
	if ctl == nil {
		return "", false, fmt.Errorf("freeze: nil tmux")
	}
	if st == nil {
		return "", false, fmt.Errorf("freeze: nil store")
	}
	ops, ok := ctl.(IdentityOps)
	if !ok {
		return "", false, fmt.Errorf("freeze: tmux adapter does not support logical identity metadata")
	}
	p, err := EnsureLiveIdentities(context.Background(), ops, name)
	if err != nil {
		return "", false, err
	}
	return FreezeSave(st, p, false)
}

func ResetActive(st store.Storer) error {
	if st == nil {
		return fmt.Errorf("reset sticky: nil store")
	}
	ensureShapesReady(st)
	if err := ensureDefault(st); err != nil {
		return err
	}
	if err := st.SetSticky("default"); err != nil {
		return fmt.Errorf("set sticky default: %w", err)
	}
	return nil
}

func Apply(tmpl *model.Session, name, root string) *model.Session {
	return bakeShape(nil, tmpl, name, root, "")
}

func ConnectProject(ctl tmux.Connector, st store.Storer, name, cwd string) error {
	if ctl == nil {
		return fmt.Errorf("connect project: nil tmux")
	}
	if name == "" {
		return fmt.Errorf("connect project: empty session name")
	}
	if ctl.Has(context.Background(), name) {
		if err := ctl.Connect(context.Background(), name, ""); err != nil {
			return fmt.Errorf("attach %q: %w", name, err)
		}
		return nil
	}
	if st != nil {
		if p, err := st.Get(name); err == nil && p != nil {
			_ = st.Touch(name)
			return ConnectPreset(ctl, st, p)
		}
	}
	tmpl, sid, err := LoadActive(st)
	if err != nil {
		return fmt.Errorf("load sticky shape: %w", err)
	}
	baked := bakeShape(st, tmpl, name, cwd, sid)
	if st == nil {
		return ConnectPreset(ctl, nil, baked)
	}
	return ConnectPreset(ctl, st, baked)
}

// ConnectPreset loads a saved/baked instance, records its baseline before an
// outside-tmux attach can replace the process, then connects the client.
func ConnectPreset(ctl tmux.Connector, st store.Storer, p *model.Session) error {
	if ctl == nil || p == nil {
		return fmt.Errorf("connect preset: nil tmux or preset")
	}
	ctx := context.Background()
	if ctl.Has(ctx, p.Name) {
		return ctl.Connect(ctx, p.Name, "")
	}
	ops, ok := ctl.(IdentityOps)
	if !ok {
		return fmt.Errorf("connect preset: tmux adapter does not support logical identity metadata")
	}
	instance := cloneSession(p)
	if st != nil {
		storedPreset, err := st.Get(p.Name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("connect preset %q identity lookup: %w", p.Name, err)
		}
		storedBaseline, err := st.GetBaseline(p.Name)
		if err != nil {
			return fmt.Errorf("connect preset %q baseline identity: %w", p.Name, err)
		}
		if storedPreset != nil {
			if !samePersistedPreset(p, storedPreset) {
				return fmt.Errorf("preset %q changed since it was selected; reload it before connecting", p.Name)
			}
			if storedPreset.LogicalID != "" {
				if p.LogicalID != "" && p.LogicalID != storedPreset.LogicalID {
					return fmt.Errorf("preset %q selection has a different logical identity than its stored instance", p.Name)
				}
				if storedBaseline != nil && storedBaseline.LogicalID != storedPreset.LogicalID {
					return fmt.Errorf("preset %q belongs to logical session %s but its baseline belongs to %s; freeze the intended replacement before loading", p.Name, storedPreset.LogicalID, storedBaseline.LogicalID)
				}
				if err := copyPresetStableIDs(instance, storedPreset); err != nil {
					return err
				}
				if !completePresetStableIDs(instance) && storedBaseline != nil && samePersistedPreset(instance, storedBaseline) {
					if err := fillMissingPresetStableIDs(instance, storedBaseline); err != nil {
						return err
					}
				}
				if !completePresetStableIDs(instance) {
					return fmt.Errorf("preset %q has incomplete stable identities; freeze it before loading", p.Name)
				}
			} else {
				clearPresetStableIDs(instance)
				if err := restorePresetIdentity(instance, storedBaseline); err != nil {
					return err
				}
			}
		} else {
			// A transient template is a new instance. Its IDs are not authoritative.
			clearPresetStableIDs(instance)
			if err := restorePresetIdentity(instance, storedBaseline); err != nil {
				return err
			}
		}
	} else {
		clearPresetStableIDs(instance)
	}
	instance.TmuxID, instance.ServerKey = "", ""
	for wi := range instance.Windows {
		instance.Windows[wi].TmuxID = ""
		for pi := range instance.Windows[wi].Panes {
			instance.Windows[wi].Panes[pi].TmuxID = ""
		}
	}
	if err := ctl.Load(ctx, instance); err != nil {
		return fmt.Errorf("load preset %q: %w", p.Name, err)
	}
	observed, err := EnsureLiveIdentitiesFromPreset(ctx, ops, p.Name, instance)
	if err != nil {
		return fmt.Errorf("load preset %q identity: %w", p.Name, err)
	}
	if st != nil {
		baseline := cloneSession(instance)
		if err := bindLoadedIDs(baseline, observed); err != nil {
			return fmt.Errorf("load preset %q layout identity: %w", p.Name, err)
		}
		baseline.SchemaVersion = model.CurrentBaselineSchemaVersion
		stampSemanticMetadata(baseline)
		if err := st.SaveBaseline(baseline); err != nil {
			return fmt.Errorf("save baseline %q: %w", p.Name, err)
		}
	}
	return ctl.Connect(ctx, instance.Name, instance.Cwd)
}

func restorePresetIdentity(preset, baseline *model.Session) error {
	if baseline == nil {
		return nil
	}
	if baseline.LogicalID == "" {
		return fmt.Errorf("preset %q has a baseline without logical identity; freeze it before loading", preset.Name)
	}
	if preset.LogicalID != "" && preset.LogicalID != baseline.LogicalID {
		return fmt.Errorf("preset %q belongs to logical session %s but its baseline belongs to %s; freeze the intended replacement before loading", preset.Name, preset.LogicalID, baseline.LogicalID)
	}
	if preset.LogicalID == "" && !samePersistedPreset(preset, baseline) {
		return fmt.Errorf("preset %q does not structurally match baseline logical session %s; freeze the intended replacement before loading", preset.Name, baseline.LogicalID)
	}
	if preset.LogicalID == "" {
		preset.LogicalID = baseline.LogicalID
	}
	if len(preset.Windows) != len(baseline.Windows) {
		return fmt.Errorf("preset %q and baseline identity tree have different window counts", preset.Name)
	}
	for wi := range preset.Windows {
		pw, bw := &preset.Windows[wi], baseline.Windows[wi]
		if pw.SlotID != "" && pw.SlotID != bw.SlotID {
			return fmt.Errorf("preset %q window %d identity conflicts with baseline", preset.Name, wi)
		}
		pw.SlotID = bw.SlotID
		if len(pw.Panes) != len(bw.Panes) {
			return fmt.Errorf("preset %q window %d and baseline identity tree have different pane counts", preset.Name, wi)
		}
		for pi := range pw.Panes {
			if pw.Panes[pi].SlotID != "" && pw.Panes[pi].SlotID != bw.Panes[pi].SlotID {
				return fmt.Errorf("preset %q window %d pane %d identity conflicts with baseline", preset.Name, wi, pi)
			}
			pw.Panes[pi].SlotID = bw.Panes[pi].SlotID
		}
	}
	return nil
}

func copyPresetStableIDs(target, source *model.Session) error {
	if target == nil || source == nil || target.Name != source.Name || len(target.Windows) != len(source.Windows) {
		return fmt.Errorf("preset instance identity tree changed")
	}
	if target.LogicalID != "" && target.LogicalID != source.LogicalID {
		return fmt.Errorf("preset %q selection has a conflicting logical identity", target.Name)
	}
	target.LogicalID = source.LogicalID
	for wi := range target.Windows {
		tw, sw := &target.Windows[wi], source.Windows[wi]
		if len(tw.Panes) != len(sw.Panes) {
			return fmt.Errorf("preset %q window %d identity tree changed", target.Name, wi)
		}
		if tw.SlotID != "" && tw.SlotID != sw.SlotID {
			return fmt.Errorf("preset %q window %d has a conflicting stable identity", target.Name, wi)
		}
		tw.SlotID = sw.SlotID
		for pi := range tw.Panes {
			if tw.Panes[pi].SlotID != "" && tw.Panes[pi].SlotID != sw.Panes[pi].SlotID {
				return fmt.Errorf("preset %q window %d pane %d has a conflicting stable identity", target.Name, wi, pi)
			}
			tw.Panes[pi].SlotID = sw.Panes[pi].SlotID
		}
	}
	return nil
}

func fillMissingPresetStableIDs(target, source *model.Session) error {
	if target == nil || source == nil || len(target.Windows) != len(source.Windows) {
		return fmt.Errorf("preset stable identity tree does not match baseline")
	}
	for wi := range target.Windows {
		if target.Windows[wi].SlotID == "" {
			target.Windows[wi].SlotID = source.Windows[wi].SlotID
		}
		if len(target.Windows[wi].Panes) != len(source.Windows[wi].Panes) {
			return fmt.Errorf("preset window %d stable identity tree does not match baseline", wi)
		}
		for pi := range target.Windows[wi].Panes {
			if target.Windows[wi].Panes[pi].SlotID == "" {
				target.Windows[wi].Panes[pi].SlotID = source.Windows[wi].Panes[pi].SlotID
			}
		}
	}
	return nil
}

func completePresetStableIDs(preset *model.Session) bool {
	if preset == nil || preset.LogicalID == "" || len(preset.Windows) == 0 {
		return false
	}
	for _, window := range preset.Windows {
		if window.SlotID == "" || len(window.Panes) == 0 {
			return false
		}
		for _, pane := range window.Panes {
			if pane.SlotID == "" {
				return false
			}
		}
	}
	return true
}

func clearPresetStableIDs(preset *model.Session) {
	if preset == nil {
		return
	}
	preset.LogicalID = ""
	for wi := range preset.Windows {
		preset.Windows[wi].SlotID = ""
		for pi := range preset.Windows[wi].Panes {
			preset.Windows[wi].Panes[pi].SlotID = ""
		}
	}
}

// Old preset rows did not persist logical IDs. Reuse baseline IDs only when
// every persisted instance field agrees in the same ordered tree; names alone
// are insufficient to infer ownership.
func samePersistedPreset(preset, baseline *model.Session) bool {
	if preset == nil || baseline == nil || preset.Name != baseline.Name || preset.Cwd != baseline.Cwd || len(preset.Windows) != len(baseline.Windows) {
		return false
	}
	for wi := range preset.Windows {
		pw, bw := preset.Windows[wi], baseline.Windows[wi]
		layoutEqual := pw.Layout == bw.Layout
		if !layoutEqual {
			layoutEqual, _ = tmuxlayout.Compare(pw.Layout, len(pw.Panes), bw.Layout, len(bw.Panes))
		}
		if pw.Idx != bw.Idx || pw.Name != bw.Name || pw.Cwd != bw.Cwd || !layoutEqual || len(pw.Panes) != len(bw.Panes) {
			return false
		}
		for pi := range pw.Panes {
			pp, bp := pw.Panes[pi], bw.Panes[pi]
			if pp.Idx != bp.Idx || pp.Cwd != bp.Cwd || pp.Cmd != bp.Cmd || pp.CmdPath != bp.CmdPath {
				return false
			}
		}
	}
	return true
}

func stampSemanticMetadata(s *model.Session) {
	if s == nil {
		return
	}
	reclassify := s.ClassifierVersion != classify.WindowRoleVersion
	s.ClassifierVersion = classify.WindowRoleVersion
	for i := range s.Windows {
		if reclassify || s.Windows[i].Role == "" {
			s.Windows[i].Role = classify.WindowRole(s.Windows[i])
		}
	}
}

func cloneSession(p *model.Session) *model.Session {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Windows = append([]model.Window(nil), p.Windows...)
	for i := range copy.Windows {
		copy.Windows[i].Panes = append([]model.Pane(nil), p.Windows[i].Panes...)
	}
	return &copy
}

func bindLoadedIDs(target, observed *model.Session) error {
	if target == nil || observed == nil {
		return fmt.Errorf("missing loaded or observed session")
	}
	if len(target.Windows) != len(observed.Windows) {
		return fmt.Errorf("loaded session has %d windows; observation has %d", len(target.Windows), len(observed.Windows))
	}
	target.LogicalID, target.TmuxID = observed.LogicalID, observed.TmuxID
	target.ServerKey = observed.ServerKey
	for wi := range target.Windows {
		tw, ow := &target.Windows[wi], &observed.Windows[wi]
		if len(tw.Panes) != len(ow.Panes) {
			return fmt.Errorf("loaded window %d has %d panes; observation has %d", wi, len(tw.Panes), len(ow.Panes))
		}
		if ow.Layout != "" {
			tw.Layout = ow.Layout
			bindings, err := tmuxlayout.PaneBindings(tw.Layout)
			if err != nil {
				return fmt.Errorf("inspect window %d layout: %w", wi, err)
			}
			if len(bindings) != 0 {
				paneIDsByIndex := make(map[int]string, len(ow.Panes))
				paneIndexes := make(map[int]int, len(ow.Panes))
				for _, pane := range ow.Panes {
					if pane.TmuxID == "" || pane.SlotID == "" {
						return fmt.Errorf("loaded window %d pane index %d has incomplete runtime/slot identity", wi, pane.Idx)
					}
					paneIDsByIndex[pane.Idx] = pane.SlotID
					paneIndexes[pane.Idx] = pane.Idx
				}
				tw.Layout, err = tmuxlayout.RemapPaneBindingsByIndex(tw.Layout, paneIDsByIndex, paneIndexes)
				if err != nil {
					return fmt.Errorf("normalize loaded window %d layout IDs to stable pane slots: %w", wi, err)
				}
			}
		}
		tw.Idx, tw.TmuxID, tw.SlotID = ow.Idx, ow.TmuxID, ow.SlotID
		for pi := range tw.Panes {
			tw.Panes[pi].Idx = ow.Panes[pi].Idx
			tw.Panes[pi].TmuxID = ow.Panes[pi].TmuxID
			tw.Panes[pi].SlotID = ow.Panes[pi].SlotID
		}
	}
	return nil
}
