package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	DataDir   string
	ConfigDir string

	PollInterval   time.Duration
	ZoxideCap      int
	MaxShow        int
	GitConcurrency int
	ProcCacheTTL   time.Duration
	PruneCutoff    time.Duration

	// Icons controls TUI chrome glyphs: "auto" (currently nerd-on, the
	// historical default), "nerd", or "ascii".
	Icons string
	// Autostart lets the picker start gotomuxd when it is not running.
	Autostart bool
	// Prewarm gates the daemon's page-cache warm: "auto" (rotational disks
	// only), "on", or "off".
	Prewarm string
}

// defaults returns the canonical value of every setting.
func defaults() *Config {
	return &Config{
		PollInterval:   10 * time.Second,
		ZoxideCap:      40,
		MaxShow:        12,
		GitConcurrency: 4,
		ProcCacheTTL:   2 * time.Second,
		PruneCutoff:    720 * time.Hour,
		Icons:          "auto",
		Autostart:      true,
		Prewarm:        "auto",
	}
}

// Load resolves settings from two layers: built-in defaults < config.toml.
//
// There is deliberately no environment layer: settings live in one inspectable
// file. The XDG_* base-directory variables keep their standard meaning, but
// they locate the installation — they are not gotomux configuration.
func Load() *Config {
	cfg := defaults()
	if fc := readFileConfig(fileConfigPath()); fc != nil {
		mergeFile(cfg, fc)
	}
	cfg.normalize()
	return cfg
}

// fileConfigPath resolves the config file location from XDG only.
//
// The file itself is deliberately not consulted: a key that relocates its own
// directory would make the effective configuration depend on read order. Move
// the tree via XDG_CONFIG_HOME instead.
func fileConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gotomux", "config.toml")
}

// fileConfig mirrors Config with pointer fields so "key absent" stays
// representable; a present-but-zero key still overrides the default.
type fileConfig struct {
	DataDir        *string       `toml:"data_dir"`
	ConfigDir      *string       `toml:"config_dir"`
	PollInterval   *tomlDuration `toml:"poll_interval"`
	ZoxideCap      *int          `toml:"zoxide_cap"`
	MaxShow        *int          `toml:"max_show"`
	GitConcurrency *int          `toml:"git_concurrency"`
	ProcCacheTTL   *tomlDuration `toml:"proc_cache_ttl"`
	PruneCutoff    *tomlDuration `toml:"prune_cutoff"`
	Icons          *string       `toml:"icons"`
	Daemon         *daemonConfig `toml:"daemon"`
}

type daemonConfig struct {
	Autostart *bool   `toml:"autostart"`
	Prewarm   *string `toml:"prewarm"`
}

// tomlDuration accepts "10s"-style values; time.Duration itself has no
// TextUnmarshaler, so TOML cannot decode into it directly.
type tomlDuration time.Duration

func (d *tomlDuration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return err
	}
	*d = tomlDuration(v)
	return nil
}

func readFileConfig(path string) *fileConfig {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "gotomux: config: %s unreadable: %v\n", path, err)
		}
		return nil
	}

	// First pass into Primitives: this fails only on broken syntax, which
	// discards the whole file. Bad *values* are isolated per key below,
	// because a plain toml.Decode into fileConfig unifies the file's keys in
	// Go map order and aborts at the first bad entry — which settings
	// survived a mistake would depend on dice.
	var raw map[string]toml.Primitive
	md, err := toml.Decode(string(b), &raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gotomux: config: %s: %v (file ignored)\n", path, err)
		return &fileConfig{}
	}

	fc := &fileConfig{}
	for _, e := range []struct {
		name string
		set  func(toml.Primitive) error
	}{
		{"data_dir", func(p toml.Primitive) error { return decodePrim(md, p, &fc.DataDir) }},
		{"config_dir", func(p toml.Primitive) error { return decodePrim(md, p, &fc.ConfigDir) }},
		{"poll_interval", func(p toml.Primitive) error { return decodePrim(md, p, &fc.PollInterval) }},
		{"zoxide_cap", func(p toml.Primitive) error { return decodePrim(md, p, &fc.ZoxideCap) }},
		{"max_show", func(p toml.Primitive) error { return decodePrim(md, p, &fc.MaxShow) }},
		{"git_concurrency", func(p toml.Primitive) error { return decodePrim(md, p, &fc.GitConcurrency) }},
		{"proc_cache_ttl", func(p toml.Primitive) error { return decodePrim(md, p, &fc.ProcCacheTTL) }},
		{"prune_cutoff", func(p toml.Primitive) error { return decodePrim(md, p, &fc.PruneCutoff) }},
		{"icons", func(p toml.Primitive) error { return decodePrim(md, p, &fc.Icons) }},
		{"daemon", func(p toml.Primitive) error {
			fc.Daemon = new(daemonConfig)
			return md.PrimitiveDecode(p, fc.Daemon)
		}},
	} {
		p, ok := raw[e.name]
		if !ok {
			continue
		}
		delete(raw, e.name)
		if err := e.set(p); err != nil {
			fmt.Fprintf(os.Stderr, "gotomux: config: %s: %v (bad entries ignored)\n", path, err)
		}
	}

	// Whatever is left in raw was never a known key; md.Undecoded() adds
	// unknown sub-keys inside [daemon], surfaced by its PrimitiveDecode.
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	for _, k := range md.Undecoded() {
		names = append(names, k.String())
	}
	if len(names) > 0 {
		slices.Sort(names)
		fmt.Fprintf(os.Stderr, "gotomux: config: %s: unknown keys ignored: %s\n",
			path, strings.Join(names, ", "))
	}
	return fc
}

// decodePrim unifies one TOML entry into a freshly allocated *T so a bad
// value can poison only its own key.
func decodePrim[T any](md toml.MetaData, p toml.Primitive, dst **T) error {
	v := new(T)
	if err := md.PrimitiveDecode(p, v); err != nil {
		return err
	}
	*dst = v
	return nil
}

// apply copies one file override when its key is present.
func apply[T any](dst *T, file *T) {
	if file != nil {
		*dst = *file
	}
}

func mergeFile(cfg *Config, fc *fileConfig) {
	apply(&cfg.DataDir, fc.DataDir)
	apply(&cfg.ConfigDir, fc.ConfigDir)
	if v := fc.PollInterval; v != nil {
		cfg.PollInterval = time.Duration(*v)
	}
	apply(&cfg.ZoxideCap, fc.ZoxideCap)
	apply(&cfg.MaxShow, fc.MaxShow)
	apply(&cfg.GitConcurrency, fc.GitConcurrency)
	if v := fc.ProcCacheTTL; v != nil {
		cfg.ProcCacheTTL = time.Duration(*v)
	}
	if v := fc.PruneCutoff; v != nil {
		cfg.PruneCutoff = time.Duration(*v)
	}
	apply(&cfg.Icons, fc.Icons)
	if fc.Daemon != nil {
		apply(&cfg.Autostart, fc.Daemon.Autostart)
		apply(&cfg.Prewarm, fc.Daemon.Prewarm)
	}
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
	switch c.Icons {
	case "nerd", "ascii":
	default:
		c.Icons = "auto"
	}
	switch c.Prewarm {
	case "on", "off":
	default:
		c.Prewarm = "auto"
	}
}

// NerdIcons reports whether TUI chrome may draw Nerd Font glyphs. "auto"
// currently resolves to yes (the historical default).
func (c *Config) NerdIcons() bool {
	return c.Icons != "ascii"
}

// SocketPath is the single source of truth for the IPC socket location.
// It used to be derived from XDG_DATA_HOME in three independent places (the CLI,
// daemon.New, and ServeIPC), none of which consulted DataDir. So relocating the
// store moved the database but not the socket, splitting client and daemon onto
// different state, and the path ensureSocket watched was computed separately
// from the one ServeIPC actually bound.
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
