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

// ProfileRun times the standalone construction path.
//
// It ends by calling newModelCore rather than re-assembling a model literal. The
// third hand-maintained copy of that literal used to live here, which meant the
// profiler could report timings for a construction sequence the real picker no
// longer used.
func ProfileRun(cfg *config.Config, ctl tmux.Connector, st store.Storer, createName, createCwd string) {
	fmt.Fprintln(os.Stderr, "  picker init:")

	log := func(name string, fn func()) {
		start := time.Now()
		fn()
		fmt.Fprintf(os.Stderr, "    %-26s %v\n", name, time.Since(start))
	}

	// Stage timings for the pieces newModelCore composes, so the breakdown stays
	// useful now that construction itself is a single call.
	var cache *sourceCache
	log("sourceCache", func() {
		cache = &sourceCache{zoxSt: st, zoxMu: &sync.Mutex{}, zoxCap: zoxCapFrom(cfg)}
	})

	var srcs []Source
	log("defaultSources", func() {
		srcs = defaultSources(ctl, st, createName, createCwd, cache)
	})

	var bySrc map[Source][]Item
	log("snapshotAll", func() {
		bySrc = snapshotAll(srcs)
	})

	var env Context
	log("newContext", func() {
		env = newContext(ctl, st)
	})

	log("applyRankMeta", func() {
		applyRankMeta(bySrc, st, env)
	})

	log("newModelCore (whole)", func() {
		_ = newModelCore(cfg, Deps{Ctl: ctl, Store: st}, createName, createCwd, Seed{})
	})
}
