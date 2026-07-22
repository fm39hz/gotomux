package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	DataDir   string `env:"GOTOMUX_DATA_DIR"`
	ConfigDir string `env:"GOTOMUX_CONFIG_DIR"`

	PollInterval    time.Duration `env:"GOTOMUX_POLL_INTERVAL" envDefault:"10s"`
	ZoxideCap      int           `env:"GOTOMUX_ZOXIDE_CAP" envDefault:"40"`
	MaxShow        int           `env:"GOTOMUX_MAX_SHOW" envDefault:"12"`
	GitConcurrency int           `env:"GOTOMUX_GIT_CONCURRENCY" envDefault:"4"`
	ProcCacheTTL   time.Duration `env:"GOTOMUX_PROC_CACHE_TTL" envDefault:"2s"`
	PruneCutoff    time.Duration `env:"GOTOMUX_PRUNE_CUTOFF" envDefault:"720h"`
}

func Load() *Config {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		// fallback to defaults (envDefault handles most fields)
		_ = err
	}
	return cfg
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
