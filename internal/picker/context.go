package picker

import (
	"time"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Context holds all environment signals for ranking and filtering.
// Built once per picker open, threaded through the pipeline instead of
// scattering individual parameters across function signatures.
type Context struct {
	Session string              // current tmux session name, "" outside tmux
	Path    string              // session path (project root), "" outside tmux
	Pairs   map[string]int64   // co-occurrence scores with current session
	Usage   map[string]store.Usage
	Now     int64
}

func newContext(ctl tmux.Connector, st *store.Store) Context {
	ctx := Context{Now: time.Now().Unix()}
	if ctl != nil {
		ctx.Session = ctl.CurrentSession()
		ctx.Path = ctl.CurrentSessionPath()
	}
	if st != nil && ctx.Session != "" {
		ctx.Pairs, _ = st.PairScores(ctx.Session, ctx.Now)
		ctx.Usage, _ = st.AllUsage()
	}
	if ctx.Session == "" {
		ctx.Pairs = nil
	}
	return ctx
}

func (ctx Context) HasSession() bool { return ctx.Session != "" }
