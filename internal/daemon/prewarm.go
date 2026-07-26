package daemon

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
)

// prewarmBudget bounds the background read so a pathological filesystem cannot
// keep the goroutine alive forever.
const prewarmBudget = 30 * time.Second

// prewarm pulls the files the CLI's cold path touches into the page cache.
//
// This exists because cold start is seek-bound, not CPU-bound, and on a spinning
// disk that dominates everything else. Measured on this project's own dev machine
// (/dev/sda1, rotational=1), with the page cache dropped for state.db and the
// gotomux/tmux/zoxide binaries:
//
//	no prewarm:  285ms / 60ms / 41ms
//	prewarmed:    13ms / 14ms / 12ms
//
// The daemon is the right place for it: it starts at login, so this I/O happens
// while nobody is waiting, and the user's first picker open finds everything
// resident. On an SSD it is a cheap no-op; the cost either way is a sequential
// read of ~15 MB into cache the CLI is about to need anyway.
//
// Note this is also why "the binary is 9.7 MB" is not worth a refactor: paging it
// in is only expensive when cold, and pre-reading it is far cheaper than
// restructuring the CLI to drop SQLite.
//
// Set GOTOMUX_NO_PREWARM=1 to skip.
func prewarm(cfg *config.Config) {
	if os.Getenv("GOTOMUX_NO_PREWARM") != "" {
		return
	}
	deadline := time.Now().Add(prewarmBudget)
	var n int64
	for _, p := range prewarmPaths(cfg) {
		if time.Now().After(deadline) {
			log.Printf("[warm] [WARN] budget exhausted; stopped early")
			break
		}
		n += readThrough(p)
	}
	if n > 0 {
		log.Printf("[warm] [INFO] paged in %d KB", n/1024)
	}
}

// prewarmPaths lists what the CLI reads before it can paint: its own binary, the
// tools it forks, and the store.
func prewarmPaths(cfg *config.Config) []string {
	var out []string
	add := func(p string) {
		if p != "" {
			out = append(out, p)
		}
	}

	// The CLI binary is the single biggest item. Prefer a sibling of this daemon,
	// which is how `go install` and the package both lay them out, and fall back to
	// PATH.
	if self, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(self), "gotomux"))
	}
	if p, err := exec.LookPath("gotomux"); err == nil {
		add(p)
	}
	for _, tool := range []string{"tmux", "zoxide"} {
		if p, err := exec.LookPath(tool); err == nil {
			add(p)
		}
	}

	if cfg != nil {
		db := filepath.Join(cfg.ResolveDataDir(), "state.db")
		add(db)
		add(db + "-wal")
	}
	return dedupExisting(out)
}

func dedupExisting(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if seen[real] {
			continue
		}
		fi, err := os.Stat(real)
		if err != nil || fi.IsDir() {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	return out
}

// readThrough streams a file to discard, which is what actually populates the page
// cache. posix_fadvise(WILLNEED) is only a hint and the kernel may ignore it.
func readThrough(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n, err := io.Copy(io.Discard, f)
	if err != nil {
		return 0
	}
	return n
}
