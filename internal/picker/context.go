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

func newContext(ctl tmux.Connector, st store.Storer) Context {
	ctx := Context{Now: time.Now().Unix()}
	if ctl != nil {
		ctx.Session = ctl.CurrentSession(context.Background())
		ctx.Path = ctl.CurrentSessionPath(context.Background())
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
