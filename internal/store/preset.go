package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store/dbgen"
)

func (s *Store) ListNames() ([]string, error) {
	ms, err := s.ListMeta()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out, nil
}

// PresetMeta is name+cwd+recency for list/dedup/rank (no full tree load).
// PresetMeta is served over IPC as well as read from SQLite, so it carries json
// tags: without them the payload mixed snake_case keys from daemon.Response with
// Go field names from the embedded types.
type PresetMeta struct {
	Name      string `json:"name"`
	Cwd       string `json:"cwd,omitempty"`
	LastUsed  int64  `json:"last_used,omitempty"`
	LogicalID string `json:"logical_id,omitempty"`
}

func (s *Store) ListMeta() ([]PresetMeta, error) {
	rows, err := s.q.ListPresetMeta(storeContext)
	if err != nil {
		return nil, err
	}
	out := make([]PresetMeta, 0, len(rows))
	for _, row := range rows {
		out = append(out, PresetMeta{Name: row.Name, Cwd: row.Cwd, LastUsed: row.LastUsed, LogicalID: row.LogicalID})
	}
	return out, nil
}

func (s *Store) Get(name string) (*model.Session, error) {
	row, err := s.q.PresetByName(storeContext, name)
	if err != nil {
		return nil, err
	}
	sess := &model.Session{Name: row.Name, Cwd: row.Cwd, LogicalID: row.LogicalID}
	wins, err := s.q.PresetWindows(storeContext, name)
	if err != nil {
		return nil, err
	}
	for _, wr := range wins {
		w := model.Window{Idx: int(wr.Idx), Name: wr.Name, Cwd: wr.Cwd, Layout: wr.Layout, SlotID: wr.SlotID}
		panes, err := s.q.PresetPanes(storeContext, wr.ID)
		if err != nil {
			return nil, err
		}
		for _, pane := range panes {
			w.Panes = append(w.Panes, model.Pane{Idx: int(pane.Idx), Cwd: pane.Cwd, Cmd: pane.Cmd, CmdPath: pane.CmdPath, StartCmd: pane.StartCmd, SlotID: pane.SlotID})
		}
		sess.Windows = append(sess.Windows, w)
	}
	return sess, nil
}

func (s *Store) GetRecipe(name string) (model.Recipe, error) {
	session, err := s.Get(name)
	if err != nil {
		return model.Recipe{}, err
	}
	return model.RecipeFromSession(*session), nil
}

func (s *Store) SaveRecipe(recipe model.Recipe) error {
	session := recipe.Session()
	return s.Save(&session)
}

func (s *Store) Save(sess *model.Session) error {
	if sess == nil {
		return fmt.Errorf("nil preset")
	}
	if !project.ValidSessionName(sess.Name) {
		return fmt.Errorf("invalid session name %q", sess.Name)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := savePresetTx(tx, sess); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveRecipeRevision writes the edited recipe, matching desired baseline
// revision, runtime bindings for unchanged slots, and journal invalidation in
// one transaction. The baseline is updated only when it belongs to this
// logical instance; unidentified and foreign legacy rows are left untouched.
func (s *Store) SaveRecipeRevision(recipe model.Recipe, oldName string) error {
	return s.saveRecipeRevision(recipe, oldName, nil)
}

func (s *Store) SaveRecipeRevisionChecked(recipe, expected model.Recipe) error {
	return s.saveRecipeRevision(recipe, expected.Name, &expected)
}

func (s *Store) saveRecipeRevision(recipe model.Recipe, oldName string, expected *model.Recipe) error {
	if !project.ValidSessionName(recipe.Name) {
		return fmt.Errorf("invalid preset name %q", recipe.Name)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if expected != nil {
		reader := &Store{q: dbgen.New(tx)}
		current, err := reader.GetRecipe(oldName)
		if err != nil {
			return fmt.Errorf("read edit revision: %w", err)
		}
		if !expected.SameReplay(current) || expected.LogicalID != current.LogicalID {
			return fmt.Errorf("recipe changed while editor was open; reopen before saving")
		}
		for wi, window := range expected.Windows {
			if window.SlotID != current.Windows[wi].SlotID {
				return fmt.Errorf("recipe window identity changed while editing")
			}
			for pi, pane := range window.Panes {
				if pane.SlotID != current.Windows[wi].Panes[pi].SlotID {
					return fmt.Errorf("recipe pane identity changed while editing")
				}
			}
		}
	}
	if err := saveRecipeTx(tx, recipe); err != nil {
		return err
	}
	if oldName != "" && oldName != recipe.Name {
		if err := dbgen.New(tx).DeleteSession(storeContext, oldName); err != nil {
			return err
		}
	}
	if recipe.LogicalID != "" {
		q := dbgen.New(tx)
		row, lookupErr := q.BaselineByLogicalID(storeContext, recipe.LogicalID)
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		if lookupErr == nil {
			old, err := decodeBaselineEnvelope(row.Body, row.LogicalID)
			if err != nil {
				return fmt.Errorf("decode baseline before recipe revision: %w", err)
			}

			previous := model.BaselineFromSession(old)
			revised := model.ReviseBaseline(recipe, previous).Session()
			if err := saveBaselineTx(tx, &revised, false); err != nil {
				return fmt.Errorf("save revised desired baseline: %w", err)
			}
			if err := q.AbortReconcilesForRevision(storeContext, dbgen.AbortReconcilesForRevisionParams{UpdatedAt: time.Now().Unix(), SessionLogicalID: recipe.LogicalID}); err != nil {
				return fmt.Errorf("invalidate old reconcile plans: %w", err)
			}
		}
	}
	return tx.Commit()
}

// BindPresetIdentity updates only the stable identity columns of an existing
// preset after its loaded tmux objects have been observed and verified.
func (s *Store) BindPresetIdentity(sess *model.Session) error {
	if sess == nil {
		return fmt.Errorf("bind preset identity: nil session")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := bindPresetIdentityTx(tx, sess); err != nil {
		return fmt.Errorf("bind preset identity: %w", err)
	}
	return tx.Commit()
}

// deleteSessionsForSave removes presets that this Save should replace.
func deleteSessionsForSave(tx *sql.Tx, name, cwd string) error {
	q := dbgen.New(tx)
	// exact name
	if err := q.DeleteSession(storeContext, name); err != nil {
		return err
	}
	// same project root -> one preset per project path
	if cwd != "" {
		if err := q.DeleteSessionsByCwdExcept(storeContext, dbgen.DeleteSessionsByCwdExceptParams{Cwd: cwd, Name: name}); err != nil {
			return err
		}
	}
	// legacy aliases: names that collapse to the same key without -/_
	key := sessionAliasKey(name)
	if key == "" {
		return nil
	}
	rows, err := q.SessionNames(storeContext)
	if err != nil {
		return err
	}
	var victims []string
	for _, n := range rows {
		if n != name && sessionAliasKey(n) == key {
			victims = append(victims, n)
		}
	}
	for _, n := range victims {
		if err := q.DeleteSession(storeContext, n); err != nil {
			return err
		}
	}
	return nil
}

// bindPresetIdentityTx stores the stable IDs observed while loading an existing
// preset. It only binds an exact persisted shape; a same-name but changed tree
// must be explicitly frozen before it can replace an existing baseline.
func bindPresetIdentityTx(tx *sql.Tx, sess *model.Session) error {
	if sess == nil || sess.LogicalID == "" {
		return fmt.Errorf("preset identity requires a session logical ID")
	}
	q := dbgen.New(tx)
	stored, err := q.PresetByName(storeContext, sess.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if stored.LogicalID != "" && stored.LogicalID != sess.LogicalID {
		return fmt.Errorf("preset %q already belongs to logical session %s", sess.Name, stored.LogicalID)
	}
	if stored.Cwd != sess.Cwd {
		return fmt.Errorf("preset %q project path changed while binding identity", sess.Name)
	}
	windows, err := q.PresetWindows(storeContext, sess.Name)
	if err != nil {
		return err
	}
	if len(windows) != len(sess.Windows) {
		return fmt.Errorf("preset %q window count changed while binding identity", sess.Name)
	}
	for i, want := range sess.Windows {
		got := windows[i]

		if got.SlotID != "" && got.SlotID != want.SlotID {
			return fmt.Errorf("preset %q window %d already has a different stable identity", sess.Name, i)
		}
		panes, err := q.PresetPanes(storeContext, got.ID)
		if err != nil {
			return err
		}
		if len(panes) != len(want.Panes) {
			return fmt.Errorf("preset %q window %d pane count changed while binding identity", sess.Name, i)
		}
		storedWindow := model.WindowSpec{Idx: int(got.Idx), Name: got.Name, Cwd: got.Cwd, Layout: got.Layout}
		for _, pane := range panes {
			storedWindow.Panes = append(storedWindow.Panes, model.PaneSpec{Idx: int(pane.Idx), Cwd: pane.Cwd, Cmd: pane.Cmd, CmdPath: pane.CmdPath, StartCmd: pane.StartCmd})
		}
		if !storedWindow.SameReplay(model.RecipeFromSession(model.Session{Windows: []model.Window{want}}).Windows[0]) {
			return fmt.Errorf("preset %q window %d replay content changed while binding identity", sess.Name, i)
		}
		for j, pane := range want.Panes {
			storedPane := panes[j]

			if storedPane.SlotID != "" && storedPane.SlotID != pane.SlotID {
				return fmt.Errorf("preset %q window %d pane %d already has a different stable identity", sess.Name, i, j)
			}
		}
	}
	if err := q.UpdatePresetLogicalID(storeContext, dbgen.UpdatePresetLogicalIDParams{
		LogicalID: sess.LogicalID, Name: sess.Name, LogicalID_2: sess.LogicalID,
	}); err != nil {
		return err
	}
	for i, window := range windows {
		want := sess.Windows[i]
		if err := q.UpdatePresetWindowSlot(storeContext, dbgen.UpdatePresetWindowSlotParams{
			SlotID: want.SlotID, ID: window.ID, SlotID_2: want.SlotID,
		}); err != nil {
			return err
		}
		panes, err := q.PresetPanes(storeContext, window.ID)
		if err != nil {
			return err
		}
		for j, pane := range panes {
			if err := q.UpdatePresetPaneSlot(storeContext, dbgen.UpdatePresetPaneSlotParams{
				SlotID: want.Panes[j].SlotID, ID: pane.ID, SlotID_2: want.Panes[j].SlotID,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// sessionAliasKey: compare names ignoring - and _ (legacy sanitize dropped _).
func sessionAliasKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r == '-' || r == '_' {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Store) Touch(name string) error {
	return s.q.TouchSession(storeContext, dbgen.TouchSessionParams{LastUsed: time.Now().Unix(), Name: name})
}

func (s *Store) Delete(name string) error {
	return s.q.DeleteSession(storeContext, name)
}

// RebindName moves ranking telemetry old->new after preset rename.
// usage rows merge (opens/kills sum, last_* max); pair endpoints rewrite.
// Best-effort for ranking only - never blocks Save.
func (s *Store) RebindName(old, newName string) error {
	old, newName = strings.TrimSpace(old), strings.TrimSpace(newName)
	if old == "" || newName == "" || old == newName {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)

	// usage: merge into new, drop old
	u, err := q.UsageByName(storeContext, old)
	if err == nil {
		if err := q.MergeUsage(storeContext, dbgen.MergeUsageParams{Name: newName, Opens: u.Opens, Kills: u.Kills, LastOpen: u.LastOpen, LastKill: u.LastKill}); err != nil {
			return err
		}
		if err := q.DeleteUsageByName(storeContext, old); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// pair: rewrite endpoints; undirected canonical a < b
	rows, err := q.PairRowsForRename(storeContext, dbgen.PairRowsForRenameParams{A: old, B: old})
	if err != nil {
		return err
	}
	type pr struct {
		a, b    string
		n, last int64
	}
	var found []pr
	for _, row := range rows {
		found = append(found, pr{a: row.A, b: row.B, n: row.N, last: row.Last})
	}
	for _, r := range found {
		if err := q.DeletePair(storeContext, dbgen.DeletePairParams{A: r.a, B: r.b}); err != nil {
			return err
		}
		a, b := r.a, r.b
		if a == old {
			a = newName
		}
		if b == old {
			b = newName
		}
		if a == b {
			continue // self-pair after rename - drop
		}
		if a > b {
			a, b = b, a
		}
		if err := q.MergePair(storeContext, dbgen.MergePairParams{A: a, B: b, N: r.n, Last: r.last}); err != nil {
			return err
		}
	}

	// transition: directed prev->next; rewrite both endpoints, drop self-loops
	trows, err := q.TransitionRowsForRename(storeContext, dbgen.TransitionRowsForRenameParams{Prev: old, Next: old})
	if err != nil {
		return err
	}
	type tr struct {
		prev, next string
		n, last    int64
	}
	var tfound []tr
	for _, row := range trows {
		tfound = append(tfound, tr{prev: row.Prev, next: row.Next, n: row.N, last: row.Last})
	}
	for _, r := range tfound {
		if err := q.DeleteTransition(storeContext, dbgen.DeleteTransitionParams{Prev: r.prev, Next: r.next}); err != nil {
			return err
		}
		prev, next := r.prev, r.next
		if prev == old {
			prev = newName
		}
		if next == old {
			next = newName
		}
		if prev == next {
			continue // self-transition after rename - drop
		}
		if err := q.MergeTransition(storeContext, dbgen.MergeTransitionParams{Prev: prev, Next: next, N: r.n, Last: r.last}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NormalizeRecipeIdentity verifies the persisted replay and binds the canonical
// logical/slot identities through the transactional store boundary.
func (s *Store) NormalizeRecipeIdentity(recipe, baseline model.Recipe) (model.Recipe, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Recipe{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := dbgen.New(tx).BaselineByLogicalID(storeContext, baseline.LogicalID)
	if err != nil {
		return model.Recipe{}, fmt.Errorf("read canonical baseline for normalization: %w", err)
	}
	canonical, err := decodeBaselineEnvelope(row.Body, row.LogicalID)
	if err != nil {
		return model.Recipe{}, err
	}
	canonicalRecipe := model.RecipeFromSession(canonical)
	// Resolve both documents against the canonical persisted tree; caller
	// identity metadata cannot replace canonical ownership.
	if _, err := model.RestoreRecipeIdentity(baseline, canonicalRecipe); err != nil {
		return model.Recipe{}, fmt.Errorf("baseline changed before normalization: %w", err)
	}
	restored, err := model.RestoreRecipeIdentity(recipe, canonicalRecipe)
	if err != nil {
		return model.Recipe{}, err
	}
	session := restored.Session()
	if err := bindPresetIdentityTx(tx, &session); err != nil {
		return model.Recipe{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Recipe{}, err
	}
	return restored, nil
}
