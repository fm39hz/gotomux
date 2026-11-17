package template

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/event"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
	"github.com/fm39hz/gotomux/internal/sessionguard"
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
	contract := model.BaselineFromSession(*s)
	frozen := contract.Session()
	baseline := &frozen
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

func FreezeRemember(ctl tmux.Connector, st store.Storer, name string) (string, bool, error) {
	return freezeRemember(ctl, st, name, model.EntityIdentity{})
}

// FreezeObserved preserves the picker selection receipt through observation
// and lock acquisition. A reused name cannot redirect this operation.
func FreezeObserved(ctl tmux.Connector, st store.Storer, name string, receipt model.EntityIdentity) (string, bool, error) {
	if receipt.RuntimeID == "" || receipt.ServerKey == "" {
		return "", false, fmt.Errorf("freeze: missing selected runtime receipt")
	}
	return freezeRemember(ctl, st, name, receipt)
}

func freezeRemember(ctl tmux.Connector, st store.Storer, name string, receipt model.EntityIdentity) (shapeID string, shapeCreated bool, err error) {
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
	snapshot, err := ops.Observe(context.Background(), name)
	if err != nil {
		return "", false, err
	}
	if err := validateIdentitySnapshot(snapshot, name); err != nil {
		return "", false, err
	}
	if receipt.RuntimeID != "" && (snapshot.TargetSessionID != receipt.RuntimeID || snapshot.ServerKey != receipt.ServerKey) {
		return "", false, fmt.Errorf("freeze: selected session incarnation changed; refresh the picker")
	}
	release, err := sessionguard.Acquire(snapshot.ServerKey, snapshot.TargetSessionID)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = release() }()
	// Discover the lock key before acquiring it, then discard that observation.
	// All ownership and recovery decisions must use a snapshot taken under the
	// same lock as reconcile, otherwise a completed intervening operation can
	// leave freeze checking stale markers or assigning IDs from stale receipts.
	locked, err := ops.Observe(context.Background(), name)
	if err != nil {
		return "", false, err
	}
	if err := validateIdentitySnapshot(locked, name); err != nil {
		return "", false, err
	}
	if locked.ServerKey != snapshot.ServerKey || locked.TargetSessionID != snapshot.TargetSessionID {
		return "", false, fmt.Errorf("session incarnation changed before freeze lock was acquired")
	}
	snapshot = locked
	if hasTargetOperationMarkers(*snapshot, core.PlanResult{}) {
		return "", false, fmt.Errorf("freeze: session contains unfinished operation objects; resume reconcile before replacing its baseline")
	}
	preferred := snapshot.Target.Session()
	p, err := EnsureLiveIdentitiesFromPreset(context.Background(), ops, name, &preferred)
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
		if err := connectLiveByName(ctl, name, cwd); err != nil {
			return fmt.Errorf("attach %q: %w", name, err)
		}
		return nil
	}
	// Project entry restores a saved recipe before baking a new shape.
	if st != nil {
		recipe, err := st.GetRecipe(name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("connect project preset lookup: %w", err)
		}
		if recipe.Name != "" {
			if filepath.Clean(project.FindProjectRoot(recipe.Cwd)) != filepath.Clean(project.FindProjectRoot(cwd)) {
				return fmt.Errorf("preset %q belongs to another project path", name)
			}
			return ConnectRecipe(ctl, st, recipe)
		}
	}
	tmpl, sid, err := LoadActive(st)
	if err != nil {
		return fmt.Errorf("load sticky shape: %w", err)
	}
	baked := bakeShape(st, tmpl, name, cwd, sid)
	return connectPreset(ctl, st, baked, connectNewProject)
}

// ConnectPreset loads a saved/baked instance, records its baseline before an
// outside-tmux attach can replace the process, then connects the client.
func ConnectPreset(ctl tmux.Connector, st store.Storer, p *model.Session) error {
	return connectPreset(ctl, st, p, connectSavedSession)
}

// ConnectRecipe enters through the ID-free preset domain type and creates a
// disposable tmux DTO only at the effect boundary.
func ConnectRecipe(ctl tmux.Connector, st store.Storer, recipe model.Recipe) error {
	session := recipe.Session()
	return connectPreset(ctl, st, &session, connectSavedSession)
}

type connectIntent uint8

const (
	connectNewProject connectIntent = iota
	connectSavedSession
)

type baselineWritePolicy uint8

const (
	baselineInitialize baselineWritePolicy = iota
	baselineRetain
	baselineBindRecipeOnly
)

func connectPreset(ctl tmux.Connector, st store.Storer, p *model.Session, intent connectIntent) (retErr error) {
	if ctl == nil || p == nil {
		return fmt.Errorf("connect preset: nil tmux or preset")
	}
	ctx := context.Background()
	// A supported rename does not create a new logical instance. Search
	// logical ownership before treating the recipe's display name as a create.
	if intent == connectSavedSession && p.LogicalID != "" {
		live, err := ctl.ListLive(ctx)
		if err != nil {
			return fmt.Errorf("connect preset logical lookup: %w", err)
		}
		candidates := make([]model.SessionCandidate, 0, len(live))
		for _, candidate := range live {
			candidates = append(candidates, model.SessionCandidate{Name: candidate.Name, Identity: model.EntityIdentity{LogicalID: candidate.LogicalID, ServerKey: candidate.ServerKey, RuntimeID: candidate.ID}})
		}
		resolved := model.ResolveSavedTarget(p.Name, model.EntityIdentity{LogicalID: p.LogicalID}, candidates)
		if resolved.Disposition == model.TargetConflictingOwners {
			return fmt.Errorf("preset logical identity has multiple live owners")
		}
		if resolved.Disposition == model.TargetAttach {
			connector, ok := ctl.(tmux.ObservedConnector)
			if !ok {
				return fmt.Errorf("tmux adapter cannot attach an observed logical owner")
			}
			return connector.ConnectObserved(ctx, resolved.Owner.Identity.RuntimeID, resolved.Owner.Identity.ServerKey)
		}
	}
	if ctl.Has(ctx, p.Name) {
		ops, ok := ctl.(IdentityOps)
		if !ok {
			return fmt.Errorf("connect preset %q: cannot verify identity of the same-name live session", p.Name)
		}
		live, err := ops.Observe(ctx, p.Name)
		if err != nil {
			return fmt.Errorf("connect preset %q: observe existing live session: %w", p.Name, err)
		}
		if err := validateIdentitySnapshot(live, p.Name); err != nil {
			return fmt.Errorf("connect preset %q: live identity graph is invalid: %w", p.Name, err)
		}
		if p.LogicalID == "" || live.Target.LogicalID != p.LogicalID {
			return fmt.Errorf("connect preset %q: same-name live session identity does not match the selected preset", p.Name)
		}
		connector, ok := ctl.(tmux.ObservedConnector)
		if !ok {
			return fmt.Errorf("connect preset %q: tmux adapter cannot connect by observed runtime identity", p.Name)
		}
		return connector.ConnectObserved(ctx, live.TargetSessionID, live.ServerKey)
	}
	ops, ok := ctl.(IdentityOps)
	if !ok {
		return fmt.Errorf("connect preset: tmux adapter does not support logical identity metadata")
	}
	recipe := model.RecipeFromSession(*p)
	instanceValue := recipe.Session()
	instance := &instanceValue
	writePolicy := baselineInitialize
	if st != nil {
		storedPreset, err := st.Get(p.Name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("connect preset %q identity lookup: %w", p.Name, err)
		}
		storedBaseline, err := st.GetBaseline(p.Name)
		if err != nil {
			return fmt.Errorf("connect preset %q baseline identity: %w", p.Name, err)
		}
		if storedBaseline != nil {
			writePolicy = baselineRetain
		}
		if storedBaseline != nil && storedBaseline.LogicalID == "" {
			// Keep an unidentified legacy baseline untouched. It cannot establish
			// ownership, but it must not prevent creating a fresh instance from a
			// valid preset; the user can freeze that instance to establish a new
			// identified baseline explicitly.
			writePolicy = baselineBindRecipeOnly
			storedBaseline = nil
			if intent == connectNewProject {
				writePolicy = baselineRetain
			}
		}
		if intent == connectNewProject && storedBaseline != nil {
			return fmt.Errorf("create project %q: an identified baseline already owns this session name; select its preset or freeze an intentional replacement", p.Name)
		}
		if storedPreset != nil {
			if intent == connectNewProject {
				writePolicy = baselineRetain
				clearPresetStableIDs(instance)
			} else {
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
					if storedBaseline != nil {
						normalized, err := st.NormalizeRecipeIdentity(model.RecipeFromSession(*instance), model.RecipeFromSession(*storedBaseline))
						if err != nil {
							return fmt.Errorf("normalize saved recipe %q: %w", p.Name, err)
						}
						normalizedSession := normalized.Session()
						instance = &normalizedSession
					}
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
	if instance.TmuxID == "" || instance.ServerKey == "" {
		return fmt.Errorf("load preset %q returned no creation receipt; refusing to assign identity", p.Name)
	}
	// The mutation phase owns both rollback and lock lifetime. It must finish
	// before an interactive attach can block until the user detaches.
	observed, err := func() (_ *model.Session, retErr error) {
		release, err := sessionguard.Acquire(instance.ServerKey, instance.TmuxID)
		if err != nil {
			return nil, fmt.Errorf("created session %s could not enter identity phase: %w", instance.TmuxID, err)
		}
		defer func() { _ = release() }()
		creationComplete := false
		defer func() {
			if retErr == nil || creationComplete {
				return
			}
			cleaner, ok := ctl.(tmux.CreatedSessionCleaner)
			if !ok {
				retErr = fmt.Errorf("%w; partial session %s on server %s remains for recovery", retErr, instance.TmuxID, instance.ServerKey)
				return
			}
			if err := cleaner.RemoveCreatedSession(ctx, instance.TmuxID, instance.ServerKey); err != nil {
				retErr = fmt.Errorf("%w; cleanup of created session %s failed: %v", retErr, instance.TmuxID, err)
			}
		}()
		observed, err := EnsureLiveIdentitiesFromPreset(ctx, ops, p.Name, instance)
		if err != nil {
			return nil, fmt.Errorf("load preset %q identity: %w", p.Name, err)
		}
		if st != nil {
			baseline := cloneSession(instance)
			if err := bindLoadedIDs(baseline, observed); err != nil {
				return nil, fmt.Errorf("load preset %q layout identity: %w", p.Name, err)
			}
			baseline.SchemaVersion = model.CurrentBaselineSchemaVersion
			stampSemanticMetadata(baseline)
			switch writePolicy {
			case baselineRetain:
				// Preserve the independently frozen desired state.
			case baselineBindRecipeOnly:
				if err := st.BindPresetIdentity(baseline); err != nil {
					return nil, fmt.Errorf("persist loaded preset identity %q: %w", p.Name, err)
				}
			case baselineInitialize:
				if err := st.SaveInitialBaseline(baseline); err != nil {
					return nil, fmt.Errorf("save baseline %q: %w", p.Name, err)
				}
			}
		}
		creationComplete = true
		return observed, nil
	}()
	if err != nil {
		return err
	}

	connector, ok := ctl.(tmux.ObservedConnector)
	if !ok {
		return fmt.Errorf("load preset %q: tmux adapter cannot connect by verified runtime identity", p.Name)
	}
	return connector.ConnectObserved(ctx, observed.TmuxID, observed.ServerKey)
}

func connectLiveByName(ctl tmux.Connector, name, expectedPath string) error {
	ctx := context.Background()
	live, err := ctl.ListLive(ctx)
	if err != nil {
		return err
	}
	var match *tmux.LiveSession
	for i := range live {
		if live[i].Name == name {
			if match != nil {
				return fmt.Errorf("live session name %q is ambiguous", name)
			}
			match = &live[i]
		}
	}
	if match == nil {
		return fmt.Errorf("live session %q disappeared; refresh and retry", name)
	}
	if expectedPath == "" || match.Path == "" {
		return fmt.Errorf("same-name live session %q has no project path evidence; refusing name-only attach", name)
	}
	wantRoot := filepath.Clean(project.FindProjectRoot(expectedPath))
	gotRoot := filepath.Clean(project.FindProjectRoot(match.Path))
	if wantRoot != gotRoot {
		return fmt.Errorf("same-name live session %q belongs to project %q, selected project is %q", name, gotRoot, wantRoot)
	}
	connector, ok := ctl.(tmux.ObservedConnector)
	if !ok {
		return fmt.Errorf("tmux adapter cannot connect by observed runtime identity")
	}
	return connector.ConnectObserved(ctx, match.ID, match.ServerKey)
}

func restorePresetIdentity(preset, baseline *model.Session) error {
	if baseline == nil {
		return nil
	}
	restored, err := model.RestoreRecipeIdentity(model.RecipeFromSession(*preset), model.RecipeFromSession(*baseline))
	if err != nil {
		return fmt.Errorf("preset %q identity: %w", preset.Name, err)
	}
	*preset = restored.Session()
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
	if target == nil || source == nil {
		return fmt.Errorf("missing recipe identity document")
	}
	return restorePresetIdentity(target, source)
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
	return preset != nil && baseline != nil && model.RecipeFromSession(*preset).SameReplay(model.RecipeFromSession(*baseline))
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
