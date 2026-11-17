package model

// BaselineRepository persists desired state and verified runtime bindings.
// Implementations own serialization and transaction boundaries.
type BaselineRepository interface {
	GetBaselineState(name string) (*Baseline, error)
	GetBaselineStateByLogicalID(id string) (*Baseline, error)
	SaveBaselineState(state Baseline) error
	CommitReconcileState(state Baseline, journalID string) error
}

// RecipeRepository stores saved session replay documents. Normalization checks
// canonical persisted evidence before binding previously omitted identities.
type RecipeRepository interface {
	NormalizeRecipeIdentity(recipe, baseline Recipe) (Recipe, error)
	GetRecipe(name string) (Recipe, error)
	SaveRecipe(recipe Recipe) error
}

type RecipeRevisionRepository interface {
	SaveRecipeRevision(recipe Recipe, oldName string) error
}

// CheckedRecipeRevisionRepository commits only against the document opened by
// the editor. Comparison and write are one persistence transaction.
type CheckedRecipeRevisionRepository interface {
	SaveRecipeRevisionChecked(recipe, expected Recipe) error
}
