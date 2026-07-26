package picker

import (
	"context"
	"time"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Context holds all environment signals for ranking and filtering.
// Built once per picker open, threaded through the pipeline instead of
// scattering individual parameters across function signatures.
type Context struct {
	Session string           // current tmux session name, "" outside tmux
	Path    string           // session path (project root), "" outside tmux
	Pairs   map[string]int64 // co-occurrence scores with current session
	Usage   map[string]store.Usage
	Now     int64
}

// newContext derives the ranking context.
//
// live is the session list already fetched for the picker. Resolving our own
// session out of it costs nothing: $TMUX carries the session index and
// LiveSession.ID carries #{session_id}. This used to call CurrentSession and
// CurrentSessionPath — two tmux forks, ~5.5ms, about half the standalone
// construction cost — for data that was already in hand. It also makes standalone
// derive the context exactly the way the daemon path does.
//
// The fork remains only as a fallback for when the id is not in the list (the
// list read failed, or we are attached to a session the picker filters out).
//
// sessID is passed in rather than read from $TMUX here: the environment is read
// once at the edge (Deps.SessionID), which keeps this function testable and keeps
// env access out of the middle of the package.
func newContext(ctl tmux.Connector, st store.Storer, live []tmux.LiveSession, sessID string) Context {
	ctx := Context{Now: time.Now().Unix()}
	if sessID != "" {
		if cur, ok := tmux.FindByID(live, sessID); ok {
			ctx.Session, ctx.Path = cur.Name, cur.Path
		} else if ctl != nil {
			ctx.Session, ctx.Path = ctl.CurrentContext(context.Background())
		}
	}
	if st != nil {
		// Usage is unconditional. It used to share the Session != "" guard with
		// Pairs, which left Context.Usage empty outside tmux; applyRankMeta then
		// silently re-queried AllUsage itself, so ranking was correct but the same
		// data was fetched down two different paths depending on where you ran.
		// Filling it here makes Context the single description of the ranking
		// inputs, which is what lets the daemon path substitute a payload for it.
		ctx.Usage, _ = st.AllUsage()
		if ctx.Session != "" {
			ctx.Pairs, _ = st.PairScores(ctx.Session, ctx.Now)
		}
	}
	// Co-occurrence is meaningless without a current session.
	if ctx.Session == "" {
		ctx.Pairs = nil
	}
	return ctx
}

func (ctx Context) HasSession() bool { return ctx.Session != "" }
