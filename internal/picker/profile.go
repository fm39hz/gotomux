package picker

import (
	"fmt"
	"os"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

func ProfileRun(cfg *config.Config, ctl tmux.Connector, st store.Storer, createName, createCwd string) {
	fmt.Fprintln(os.Stderr, "  picker init:")

	log := func(name string, fn func()) {
		start := time.Now()
		fn()
		fmt.Fprintf(os.Stderr, "    %-26s %v\n", name, time.Since(start))
	}

	var cache *sourceCache
	log("sourceCache+uicfg", func() {
		cache = &sourceCache{zoxSt: st, zoxMu: &sync.Mutex{}}
		applyUICfg(cfg)
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

	log("StickyLabel", func() {
		template.StickyLabel(st)
	})

	log("refilter", func() {
		m := model{
			sources:    srcs,
			bySrc:      bySrc,
			cache:      cache,
			ctl:        ctl,
			store:      st,
			cfg:        cfg,
			tmpl:       "",
			env:        env,
			createName: createName,
			createCwd:  createCwd,
			ui: viewModel{
				queryInput: initInput(),
				helpModel:  help.New(),
				maxShow:    maxShow(cfg),
				started:    time.Now(),
			},
		}
		m.refilter()
	})
}
