package reconcile

import (
	"fmt"
	"strconv"

	"github.com/fm39hz/gotomux/internal/model"
)

// Policy controls how the planner treats live objects outside the desired
// baseline. Exact retains the existing --hard behavior and is destructive.
type Policy string

const (
	PolicyPreserve Policy = "preserve"
	PolicyExact    Policy = "exact"
)

func (p Policy) Validate() error {
	switch p {
	case PolicyPreserve, PolicyExact:
		return nil
	default:
		return fmt.Errorf("unknown reconcile policy %q", p)
	}
}

type ObjectKind = model.ObjectKind

const (
	ObjectSession = model.ObjectSession
	ObjectWindow  = model.ObjectWindow
	ObjectPane    = model.ObjectPane
)

// RuntimeRef is meaningful only in the server generation in which tmux
// issued the ID. Names and indices are intentionally absent.
type RuntimeRef struct {
	ServerGeneration string
	Kind             ObjectKind
	ID               string
}

func (r RuntimeRef) Validate() error {
	if r.ServerGeneration == "" {
		return fmt.Errorf("runtime reference has no server generation")
	}
	prefix := ""
	switch r.Kind {
	case ObjectSession:
		prefix = "$"
	case ObjectWindow:
		prefix = "@"
	case ObjectPane:
		prefix = "%"
	default:
		return fmt.Errorf("runtime reference has invalid object kind %q", r.Kind)
	}
	if len(r.ID) < 2 || r.ID[:1] != prefix {
		return fmt.Errorf("runtime reference has invalid %s ID %q", r.Kind, r.ID)
	}
	value, err := strconv.Atoi(r.ID[1:])
	if err != nil || value < 0 || strconv.Itoa(value) != r.ID[1:] {
		return fmt.Errorf("runtime reference has malformed %s ID %q", r.Kind, r.ID)
	}
	return nil
}

// EvidenceClass defines precedence, not a numeric score to be summed with
// weaker evidence. Resolver comparisons are lexicographic by this class.
type EvidenceClass uint8

const (
	EvidenceNone EvidenceClass = iota
	EvidenceSemantic
	EvidenceRuntime
	EvidenceLogical
)

type ResolutionStatus string

const (
	ResolutionReady     ResolutionStatus = "ready"
	ResolutionAmbiguous ResolutionStatus = "ambiguous"
	ResolutionConflict  ResolutionStatus = "conflict"
)

type SnapshotStatus string

const (
	SnapshotComplete   SnapshotStatus = "complete"
	SnapshotIncomplete SnapshotStatus = "incomplete"
)

type OperationID string

type DesiredState struct {
	Baseline          model.Baseline
	ClassifierVersion int
}

type Evidence struct {
	Class  EvidenceClass
	Signal string
	Value  string
}

type ObjectMatch struct {
	Kind               ObjectKind
	BaselineIndex      int
	BaselineIdentityID string
	LiveIdentityID     string
	BaselineRuntimeID  string
	LiveRuntimeID      string
	OutsideTarget      bool
	Evidence           []Evidence
}

type PaneResolution struct {
	Matches []ObjectMatch
	Missing []int
	Extra   []string
}

type WindowResolution struct {
	Match ObjectMatch
	Panes PaneResolution
}

type Conflict struct {
	Baseline string
	Live     []string
	Reason   string
}

type Ambiguity struct {
	Baseline        string
	Live            []string
	Reason          string
	Evidence        []Evidence
	EquivalentGroup bool
}

type Resolution struct {
	Status        ResolutionStatus
	ServerKey     string
	TargetSession ObjectMatch
	Windows       []WindowResolution
	Missing       []int
	Extra         []string
	Ambiguous     []Ambiguity
	Conflicts     []Conflict
}

type PlanStatus string

const (
	PlanNoop    PlanStatus = "noop"
	PlanReady   PlanStatus = "ready"
	PlanBlocked PlanStatus = "blocked"
)

type OperationKind string

const (
	OperationBindIdentity OperationKind = "bind-identity"
	OperationMoveWindow   OperationKind = "move-window"
	OperationLinkWindow   OperationKind = "link-window"
	OperationCreateWindow OperationKind = "create-window"
	OperationAddPane      OperationKind = "add-pane"
	OperationApplyLayout  OperationKind = "apply-layout"
	OperationSelectWindow OperationKind = "select-window"
	OperationExactRebuild OperationKind = "exact-rebuild"
)

type Operation struct {
	ID                   string
	Kind                 OperationKind
	Target               RuntimeRef
	TargetIdentityID     string
	ExpectedServerKey    string
	ExpectedSessionID    string
	ExpectedParentID     string
	ExpectedFingerprint  string
	FromIndex            int
	ToIndex              int
	Index                int
	Temporary            bool
	Destructive          bool
	ExpectedPaneCount    int
	ExpectedPaneIDs      []string
	ExpectedPaneSlotIDs  []string
	Window               model.Window
	Pane                 model.Pane
	DesiredBaseline      model.Baseline
	ExactOriginalWindows []model.Window
	Reason               string
}

type PlanResult struct {
	// PlanSchemaVersion versions the persisted JSON contract. A zero value is
	// legacy data and must pass an explicit, cursor-aware upgrade before use.
	PlanSchemaVersion        int `json:"plan_schema_version"`
	ID                       string
	Status                   PlanStatus
	Policy                   Policy
	DesiredBaseline          model.Baseline
	ClassifierVersion        int
	BlockedReason            string
	StartSnapshotFingerprint string
	FinalActiveSlotID        string
	Operations               []Operation
	PreservedWindows         []string
	PreservedPanes           []string
	Resolution               Resolution
}

const CurrentPlanSchemaVersion = 2
