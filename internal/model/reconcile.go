package model

// CurrentBaselineSchemaVersion is the newest serialized baseline contract.
// Reconcile requires the session and every baseline slot to carry an identity.
const CurrentBaselineSchemaVersion = 3

// Snapshot is a complete, server-scoped observation used by reconciliation.
// Target contains detailed pane state for the selected session; the identity
// and membership indexes cover every session/window/pane on the server.
type Snapshot struct {
	// ServerKey scopes every TmuxID in this observation.
	ServerKey string
	// Complete is true only after all server/session/window/pane references
	// validate and the selected session remains stable through observation.
	Complete        bool
	Target          Session
	TargetSessionID string
	ActiveWindowID  string
	// Sessions and Objects index logical/runtime identities server-wide.
	Sessions []SessionIdentity
	Objects  []ObjectIdentity
	// Memberships models linked windows independently from window identity.
	Memberships []WindowMembership
}

type SessionIdentity struct {
	Name        string
	LogicalID   string
	OperationID string
	RuntimeID   string
}

type ObjectKind string

const (
	ObjectSession ObjectKind = "session"
	ObjectWindow  ObjectKind = "window"
	ObjectPane    ObjectKind = "pane"
)

type ObjectIdentity struct {
	Kind ObjectKind
	// LogicalID applies to sessions. SlotID applies to baseline windows/panes.
	LogicalID   string
	SlotID      string
	OperationID string
	RuntimeID   string
	ParentID    string
	Layout      string
	PaneCount   int
}

type WindowMembership struct {
	SessionID string
	WindowID  string
	Index     int
}

type ReconcileJournalStatus string

const (
	JournalPlanned        ReconcileJournalStatus = "planned"
	JournalRunning        ReconcileJournalStatus = "running"
	JournalNeedsAttention ReconcileJournalStatus = "needs_attention"
	JournalComplete       ReconcileJournalStatus = "complete"
	JournalAborted        ReconcileJournalStatus = "aborted"
)

type ReconcileJournal struct {
	ID               string
	SessionKey       string
	SessionName      string
	SessionLogicalID string
	Policy           string
	Status           ReconcileJournalStatus
	PlanJSON         string
	CompletedStep    int
	LastError        string
	CreatedAt        int64
	UpdatedAt        int64
}
