package model

type Session struct {
	Name          string
	Cwd           string
	Windows       []Window
	ServerKey     string `json:"server_key,omitempty"`
	SchemaVersion int    `json:"baseline_version,omitempty"`
}

type Window struct {
	Idx    int
	Name   string
	Cwd    string
	Layout string
	Panes  []Pane
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
	TmuxID   string `json:"tmux_id,omitempty"`
}

type Usage struct {
	Name     string
	Opens    int64
	Kills    int64
	LastOpen int64
	LastKill int64
}
