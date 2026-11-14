package model

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
