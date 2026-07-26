package picker

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// ProfileRun times the standalone construction path, stage by stage.
//
// The stages are timed as ONE pass, not timed individually and then repeated by a
// whole-construction call. Doing both double-counted everything: the reported
// total was roughly twice the real one-pass cost, which is exactly the kind of
// number that sends you optimising the wrong thing.
//
// Note this path never consults the daemon — it is the fallback path by
// construction. Use GOTOMUX_TRACE=1 on a real run to see the daemon path.
func ProfileRun(cfg *config.Config, ctl tmux.Connector, st store.Storer, createName, createCwd string) {
	fmt.Fprintln(os.Stderr, "  picker init (standalone path, one pass):")

	stage := func(name string, fn func()) {
		start := time.Now()
		fn()
		fmt.Fprintf(os.Stderr, "    %-26s %v\n", name, time.Since(start))
	}
	total := time.Now()

	var cache *sourceCache
	stage("sourceCache", func() {
		cache = &sourceCache{zoxSt: st, zoxMu: &sync.Mutex{}, zoxCap: zoxCapFrom(cfg)}
	})

	var srcs []Source
	stage("defaultSources (ListLive)", func() {
		srcs = defaultSources(ctl, st, createName, createCwd, cache)
	})

	var bySrc map[Source][]Item
	stage("snapshotAll (zoxide+presets)", func() {
		bySrc = snapshotAll(srcs)
	})

	var env Context
	stage("newContext", func() {
		env = newContext(ctl, st, cache.tmuxSnap, tmux.CurrentSessionID())
	})

	stage("applyRankMeta", func() {
		applyRankMeta(bySrc, st, env)
	})

	var m model
	stage("assemble+refilter", func() {
		m = assemble(cfg, Deps{Ctl: ctl, Store: st, SessionID: tmux.CurrentSessionID()}, createName, createCwd, cache, srcs, bySrc, env, "")
	})

	stage("enrichVisible (git)", func() {
		m.enrichVisible()
	})

	fmt.Fprintf(os.Stderr, "    %-26s %v\n", "== one-pass total", time.Since(total))
}
