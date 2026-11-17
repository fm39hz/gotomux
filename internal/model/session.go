package model

import "path/filepath"

// EntityIdentity carries identity evidence for a session and, where relevant,
// its project target. LogicalID survives renames; RuntimeID is scoped by
// ServerKey. ProjectPath identifies project-level actions such as Create and
// Zoxide, never a saved session recipe.
type EntityIdentity struct {
	LogicalID   string
	ServerKey   string
	RuntimeID   string
	ProjectPath string
}

// IdentityRelation distinguishes missing evidence from contradictory identity.
type IdentityRelation uint8

const (
	IdentityUnknown IdentityRelation = iota
	IdentitySame
	IdentityDifferent
	IdentityConflict
)

// CompareSession compares session identity only. Project membership is a
// separate relation and cannot establish ownership of a session.
func (id EntityIdentity) CompareSession(other EntityIdentity) IdentityRelation {
	if id.LogicalID != "" && other.LogicalID != "" {
		if id.LogicalID != other.LogicalID && id.ServerKey != "" && id.ServerKey == other.ServerKey && id.RuntimeID != "" && id.RuntimeID == other.RuntimeID {
			return IdentityConflict
		}
		if id.LogicalID == other.LogicalID {
			return IdentitySame
		}
		return IdentityDifferent
	}
	if id.ServerKey != "" && other.ServerKey != "" && id.RuntimeID != "" && other.RuntimeID != "" {
		if id.ServerKey == other.ServerKey && id.RuntimeID == other.RuntimeID {
			return IdentitySame
		}
		return IdentityDifferent
	}
	return IdentityUnknown
}

func (id EntityIdentity) SameProject(other EntityIdentity) bool {
	return id.ProjectPath != "" && other.ProjectPath != "" && filepath.Clean(id.ProjectPath) == filepath.Clean(other.ProjectPath)
}

// SameEntity retains the project-action projection contract. Session callers
// should use CompareSession so insufficient evidence remains explicit.
func (id EntityIdentity) SameEntity(other EntityIdentity) bool {
	relation := id.CompareSession(other)
	if relation != IdentityUnknown {
		return relation == IdentitySame
	}
	idHasSession := id.LogicalID != "" || (id.ServerKey != "" && id.RuntimeID != "")
	otherHasSession := other.LogicalID != "" || (other.ServerKey != "" && other.RuntimeID != "")
	return !(idHasSession && otherHasSession) && id.SameProject(other)
}

type Session struct {
	Name    string
	Cwd     string
	Windows []Window
	// LogicalID persists across supported session renames. TmuxID
	// is tmux's session_id and is valid only within ServerKey's generation.
	LogicalID         string `json:"logical_id,omitempty"`
	TmuxID            string `json:"tmux_id,omitempty"`
	ServerKey         string `json:"server_key,omitempty"`
	SchemaVersion     int    `json:"baseline_version,omitempty"`
	ClassifierVersion int    `json:"classifier_version,omitempty"`
}

type Window struct {
	Idx    int
	Name   string
	Cwd    string
	Layout string
	Panes  []Pane
	Role   string `json:"semantic_role,omitempty"`
	// SlotID identifies this required baseline window; TmuxID is the live tmux window_id.
	SlotID string `json:"slot_id,omitempty"`
	TmuxID string `json:"tmux_id,omitempty"`
}

type Pane struct {
	Idx int
	Cwd string
	Cmd string // detected command without arguments
	// CmdPath is the resolved absolute executable behind Cmd, captured from
	// /proc at freeze time. Replay uses it because the pane is spawned by the
	// tmux server with the creating client's environment — a bare name the
	// user's interactive shell resolves via config-added PATH entries may not
	// exist there, which killed the pane within milliseconds and silently
	// dropped the window from rebuilt sessions.
	CmdPath  string
	StartCmd string // the command the pane was started with (keeps args)
	// SlotID identifies this required baseline pane; TmuxID is the live tmux pane_id.
	SlotID string `json:"slot_id,omitempty"`
	TmuxID string `json:"tmux_id,omitempty"`
}

type Usage struct {
	Name     string
	Opens    int64
	Kills    int64
	LastOpen int64
	LastKill int64
}
