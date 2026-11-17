package model

// Recipe is the desired session tree. It deliberately has no tmux handles or
// server generation: editing a command or recreating a session cannot change
// its logical lineage. Session is the compatibility DTO at older boundaries.
type Recipe struct {
	Name      string
	Cwd       string
	LogicalID string
	Windows   []WindowSpec
}

type WindowSpec struct {
	Idx    int
	Name   string
	Cwd    string
	Layout string
	Role   string
	SlotID string
	Panes  []PaneSpec
}

type PaneSpec struct {
	Idx      int
	Cwd      string
	Cmd      string
	CmdPath  string
	StartCmd string
	SlotID   string
}

func (w WindowSpec) SessionDTO() Window {
	window:=Window{Idx:w.Idx,Name:w.Name,Cwd:w.Cwd,Layout:w.Layout,Role:w.Role,SlotID:w.SlotID}
	for _,pane:=range w.Panes { window.Panes=append(window.Panes,pane.SessionDTO()) }
	return window
}

func (p PaneSpec) SessionDTO() Pane {
	return Pane{Idx:p.Idx,Cwd:p.Cwd,Cmd:p.Cmd,CmdPath:p.CmdPath,StartCmd:p.StartCmd,SlotID:p.SlotID}
}

// RuntimeBindings is one disposable observation of a recipe's incarnation.
// Every handle in the maps is valid only within ServerKey. The maps are keyed
// by stable slot identity, never by an index or display name.
type RuntimeBindings struct {
	ServerKey string
	SessionID string
	Windows   map[string]string
	Panes     map[string]string
}

// Baseline keeps desired state and last observed incarnation separate. A
// successful reconcile may refresh Bindings, but must not overwrite Desired
// with a live observation or a separately edited preset recipe.
type Baseline struct {
	Desired           Recipe
	Bindings          RuntimeBindings
	SchemaVersion     int
	ClassifierVersion int
}

// RecipeFromSession is an explicit DTO conversion. All slices are copied;
// runtime metadata is dropped even when the source came from tmux.
func RecipeFromSession(session Session) Recipe {
	recipe := Recipe{Name: session.Name, Cwd: session.Cwd, LogicalID: session.LogicalID}
	for _, w := range session.Windows {
		window := WindowSpec{Idx: w.Idx, Name: w.Name, Cwd: w.Cwd, Layout: w.Layout, Role: w.Role, SlotID: w.SlotID}
		for _, p := range w.Panes {
			window.Panes = append(window.Panes, PaneSpec{Idx: p.Idx, Cwd: p.Cwd, Cmd: p.Cmd, CmdPath: p.CmdPath, StartCmd: p.StartCmd, SlotID: p.SlotID})
		}
		recipe.Windows = append(recipe.Windows, window)
	}
	return recipe
}

// Session returns an independent DTO for legacy shape/render and tmux ports.
// It cannot contain runtime handles; callers must supply explicit bindings.
func (recipe Recipe) Session() Session {
	session := Session{Name: recipe.Name, Cwd: recipe.Cwd, LogicalID: recipe.LogicalID}
	for _, w := range recipe.Windows {
		window := Window{Idx: w.Idx, Name: w.Name, Cwd: w.Cwd, Layout: w.Layout, Role: w.Role, SlotID: w.SlotID}
		for _, p := range w.Panes {
			window.Panes = append(window.Panes, Pane{Idx: p.Idx, Cwd: p.Cwd, Cmd: p.Cmd, CmdPath: p.CmdPath, StartCmd: p.StartCmd, SlotID: p.SlotID})
		}
		session.Windows = append(session.Windows, window)
	}
	return session
}

func BaselineFromSession(session Session) Baseline {
	bindings := RuntimeBindings{ServerKey: session.ServerKey, SessionID: session.TmuxID, Windows: map[string]string{}, Panes: map[string]string{}}
	for _, window := range session.Windows {
		if window.SlotID != "" && window.TmuxID != "" {
			bindings.Windows[window.SlotID] = window.TmuxID
		}
		for _, pane := range window.Panes {
			if pane.SlotID != "" && pane.TmuxID != "" {
				bindings.Panes[pane.SlotID] = pane.TmuxID
			}
		}
	}
	return Baseline{Desired: RecipeFromSession(session), Bindings: bindings, SchemaVersion: session.SchemaVersion, ClassifierVersion: session.ClassifierVersion}
}

// ReviseBaseline installs an explicitly edited recipe while carrying runtime
// handles only for slots that survived the revision. Handles for changed or
// newly created slots are discarded as stale.
func ReviseBaseline(recipe Recipe, previous Baseline) Baseline {
	bindings := RuntimeBindings{ServerKey: previous.Bindings.ServerKey, Windows: map[string]string{}, Panes: map[string]string{}}
	if recipe.LogicalID == previous.Desired.LogicalID {
		bindings.SessionID = previous.Bindings.SessionID
	}
	for _, window := range recipe.Windows {
		if id := previous.Bindings.Windows[window.SlotID]; id != "" {
			bindings.Windows[window.SlotID] = id
		}
		for _, pane := range window.Panes {
			if id := previous.Bindings.Panes[pane.SlotID]; id != "" {
				bindings.Panes[pane.SlotID] = id
			}
		}
	}
	return Baseline{Desired: recipe, Bindings: bindings, SchemaVersion: CurrentBaselineSchemaVersion, ClassifierVersion: previous.ClassifierVersion}
}

// Session projects a baseline into the version-3 persisted DTO without sharing
// mutable state with either the desired tree or runtime binding maps.
func (baseline Baseline) Session() Session {
	session := baseline.Desired.Session()
	session.SchemaVersion, session.ClassifierVersion = baseline.SchemaVersion, baseline.ClassifierVersion
	session.ServerKey, session.TmuxID = baseline.Bindings.ServerKey, baseline.Bindings.SessionID
	for wi := range session.Windows {
		w := &session.Windows[wi]
		w.TmuxID = baseline.Bindings.Windows[w.SlotID]
		for pi := range w.Panes {
			p := &w.Panes[pi]
			p.TmuxID = baseline.Bindings.Panes[p.SlotID]
		}
	}
	return session
}

// ObservedSession is a live tmux observation. Its runtime handles are current
// facts, and its commands/cwds may change independently of any recipe.
// It is intentionally a distinct type so it cannot be passed as desired state.
type ObservedSession Session

func ObserveSession(session Session) ObservedSession {
	copy := session
	copy.Windows = append([]Window(nil), session.Windows...)
	for i := range copy.Windows {
		copy.Windows[i].Panes = append([]Pane(nil), session.Windows[i].Panes...)
	}
	return ObservedSession(copy)
}

// Session is the explicit compatibility projection for older tmux/store ports.
func (observed ObservedSession) Session() Session { return Session(ObserveSession(Session(observed))) }
