package picker

import (
	"fmt"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/store"
)

// BenchmarkNewModelSeeded measures the daemon path's only real work: turning a
// payload into a ranked list. No I/O is involved by design.
func BenchmarkNewModelSeeded(b *testing.B) {
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40, GitConcurrency: 4}
	f := newFixture()
	ctl, _ := f.doubles()
	deps := Deps{Ctl: ctl}
	env := &Context{Usage: f.usage}
	for b.Loop() {
		_ = NewModelFromDaemon(cfg, deps, "newproj", "/w/newproj", f.seed(env))
	}
}

// BenchmarkNewModelSeededRealistic uses payload sizes seen on a real machine
// (3 sessions, 9 presets, 208 zoxide rows), so the number is comparable to the
// cold-start budget rather than to a toy fixture.
func BenchmarkNewModelSeededRealistic(b *testing.B) {
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40, GitConcurrency: 4}
	f := newFixture()
	for i := len(f.zox); i < 208; i++ {
		f.zox = append(f.zox, store.ZoxRow{
			Name:    fmt.Sprintf("proj%03d", i),
			Path:    fmt.Sprintf("/w/proj%03d", i),
			Title:   fmt.Sprintf("[Zoxide] proj%03d", i),
			Recency: int64(208 - i),
		})
	}
	for i := len(f.presets); i < 9; i++ {
		f.presets = append(f.presets, store.PresetMeta{
			Name: fmt.Sprintf("preset%d", i), Cwd: fmt.Sprintf("/w/preset%d", i), LastUsed: int64(600 - i),
		})
	}
	ctl, _ := f.doubles()
	deps := Deps{Ctl: ctl}
	env := &Context{Usage: f.usage}
	for b.Loop() {
		_ = NewModelFromDaemon(cfg, deps, "newproj", "/w/newproj", f.seed(env))
	}
}

// BenchmarkRefilterRealistic is the per-keystroke cost.
func BenchmarkRefilterRealistic(b *testing.B) {
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40}
	f := newFixture()
	for i := len(f.zox); i < 208; i++ {
		f.zox = append(f.zox, store.ZoxRow{
			Name: fmt.Sprintf("proj%03d", i), Path: fmt.Sprintf("/w/proj%03d", i),
			Recency: int64(208 - i),
		})
	}
	ctl, _ := f.doubles()
	m := NewModelFromDaemon(cfg, Deps{Ctl: ctl}, "newproj", "/w/newproj",
		f.seed(&Context{Usage: f.usage}))
	m.ui.queryInput.SetValue("pro")
	for b.Loop() {
		m.refilterFromQuery()
	}
}
