package config

import (
	"testing"
	"time"
)

// TestSocketPathFollowsDataDir pins the fix for the socket being derived from
// XDG_DATA_HOME in three places that all ignored DataDir: GOTOMUX_DATA_DIR moved
// the store but not the socket, so client and daemon ended up on different state.
func TestSocketPathFollowsDataDir(t *testing.T) {
	t.Setenv("GOTOMUX_DATA_DIR", "/tmp/zz-datadir")
	c := Load()
	if got, want := c.ResolveDataDir(), "/tmp/zz-datadir/gotomux"; got != want {
		t.Errorf("ResolveDataDir = %q, want %q", got, want)
	}
	if got, want := c.SocketPath(), "/tmp/zz-datadir/gotomux/gotomux.sock"; got != want {
		t.Errorf("SocketPath = %q, want %q", got, want)
	}
}

func TestSocketPathFollowsXDG(t *testing.T) {
	t.Setenv("GOTOMUX_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "/tmp/zz-xdg")
	c := Load()
	if got, want := c.SocketPath(), "/tmp/zz-xdg/gotomux/gotomux.sock"; got != want {
		t.Errorf("SocketPath = %q, want %q", got, want)
	}
}

// TestBadDurationDoesNotZeroPollInterval: env.Parse leaves an unparseable
// duration at zero, and PollInterval == 0 makes the daemon's ticker spin as a
// busy loop. Load must repair it rather than swallow the error.
func TestBadDurationDoesNotZeroPollInterval(t *testing.T) {
	t.Setenv("GOTOMUX_POLL_INTERVAL", "definitely-not-a-duration")
	c := Load()
	if c.PollInterval < time.Second {
		t.Errorf("PollInterval = %v; a sub-second value turns the poll into a busy loop", c.PollInterval)
	}
}

func TestNormalizeFillsZeroes(t *testing.T) {
	c := &Config{}
	c.normalize()
	for name, got := range map[string]int{
		"ZoxideCap":      c.ZoxideCap,
		"MaxShow":        c.MaxShow,
		"GitConcurrency": c.GitConcurrency,
	} {
		if got <= 0 {
			t.Errorf("%s = %d after normalize, want > 0", name, got)
		}
	}
	if c.PollInterval <= 0 || c.ProcCacheTTL <= 0 || c.PruneCutoff <= 0 {
		t.Errorf("durations not normalized: poll=%v proc=%v prune=%v",
			c.PollInterval, c.ProcCacheTTL, c.PruneCutoff)
	}
}
