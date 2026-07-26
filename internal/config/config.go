package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	DataDir   string `env:"GOTOMUX_DATA_DIR"`
	ConfigDir string `env:"GOTOMUX_CONFIG_DIR"`

	PollInterval   time.Duration `env:"GOTOMUX_POLL_INTERVAL" envDefault:"10s"`
	ZoxideCap      int           `env:"GOTOMUX_ZOXIDE_CAP" envDefault:"40"`
	MaxShow        int           `env:"GOTOMUX_MAX_SHOW" envDefault:"12"`
	GitConcurrency int           `env:"GOTOMUX_GIT_CONCURRENCY" envDefault:"4"`
	ProcCacheTTL   time.Duration `env:"GOTOMUX_PROC_CACHE_TTL" envDefault:"2s"`
	PruneCutoff    time.Duration `env:"GOTOMUX_PRUNE_CUTOFF" envDefault:"720h"`
}

func Load() *Config {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		// A parse failure leaves the affected field at its zero value, which for a
		// duration means 0 — and PollInterval == 0 turns the daemon's poll into a
		// busy loop. Say so instead of swallowing it, then repair below.
		fmt.Fprintf(os.Stderr, "gotomux: config: %v (using defaults for unparsed fields)\n", err)
	}
	cfg.normalize()
	return cfg
}

// normalize repairs values that would otherwise be actively harmful rather than
// merely unset.
func (c *Config) normalize() {
	if c.PollInterval < time.Second {
		c.PollInterval = 10 * time.Second
	}
	if c.ZoxideCap <= 0 {
		c.ZoxideCap = 40
	}
	if c.MaxShow <= 0 {
		c.MaxShow = 12
	}
	if c.GitConcurrency <= 0 {
		c.GitConcurrency = 4
	}
	if c.ProcCacheTTL <= 0 {
		c.ProcCacheTTL = 2 * time.Second
	}
	if c.PruneCutoff <= 0 {
		c.PruneCutoff = 720 * time.Hour
	}
}

// SocketPath is the single source of truth for the IPC socket location.
//
// It used to be derived from XDG_DATA_HOME in three independent places (the CLI,
// daemon.New, and ServeIPC), none of which consulted DataDir. So GOTOMUX_DATA_DIR
// moved the store but not the socket, splitting client and daemon onto different
// state, and the path ensureSocket watched was computed separately from the one
// ServeIPC actually bound.
func (c *Config) SocketPath() string {
	return filepath.Join(c.ResolveDataDir(), "gotomux.sock")
}

func (c *Config) ResolveDataDir() string {
	if c.DataDir != "" {
		return filepath.Join(c.DataDir, "gotomux")
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "gotomux")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "gotomux")
}

func (c *Config) ResolveConfigDir() string {
	if c.ConfigDir != "" {
		return filepath.Join(c.ConfigDir, "gotomux")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gotomux")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gotomux")
}
