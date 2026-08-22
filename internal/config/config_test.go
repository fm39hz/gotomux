package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig points XDG_CONFIG_HOME at a fresh temp dir and drops config.toml
// into it, so Load() sees exactly this file and nothing from the developer.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "gotomux"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gotomux", "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileSetsValues(t *testing.T) {
	writeConfig(t, `
data_dir = "/tmp/zz-data"
max_show = 7
poll_interval = "3s"
icons = "ascii"
[daemon]
autostart = false
prewarm = "on"
`)
	c := Load()
	if got, want := c.ResolveDataDir(), "/tmp/zz-data/gotomux"; got != want {
		t.Errorf("ResolveDataDir = %q, want %q", got, want)
	}
	if c.MaxShow != 7 || c.PollInterval != 3*time.Second {
		t.Errorf("scalars not applied: max_show=%d poll=%v", c.MaxShow, c.PollInterval)
	}
	if c.NerdIcons() {
		t.Errorf(`icons = "ascii" must disable nerd glyphs`)
	}
	if c.Autostart {
		t.Errorf("autostart = false not applied")
	}
	if c.Prewarm != "on" {
		t.Errorf("prewarm = %q, want %q", c.Prewarm, "on")
	}
}

func TestMissingFileUsesDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := Load()
	if c.MaxShow != 12 || !c.NerdIcons() || !c.Autostart ||
		c.Prewarm != "auto" || c.PollInterval != 10*time.Second {
		t.Errorf("defaults wrong without config.toml: %+v", c)
	}
}

func TestMalformedFileFallsBackToDefaults(t *testing.T) {
	writeConfig(t, "this is [ not toml ===")
	c := Load()
	if c.MaxShow != 12 || c.PollInterval != 10*time.Second {
		t.Errorf("malformed file must degrade to repaired defaults: max=%d poll=%v",
			c.MaxShow, c.PollInterval)
	}
}

// A bad duration must not poison keys parsed before it, and the zero it would
// leave behind is normalized away — PollInterval == 0 makes the daemon's ticker
// spin as a busy loop.
func TestBadDurationInFileFallsBackToDefault(t *testing.T) {
	writeConfig(t, "max_show = 7\npoll_interval = \"definitely-not-a-duration\"\n")
	c := Load()
	if c.MaxShow != 7 {
		t.Errorf("valid key before the bad one not applied: %d", c.MaxShow)
	}
	if c.PollInterval < time.Second {
		t.Errorf("PollInterval = %v; a sub-second value turns the poll into a busy loop",
			c.PollInterval)
	}
}

// A typo'd key must not poison the rest of the file, but it must be named on
// stderr so the mistake does not silently do nothing.
func TestUnknownKeysIgnoredButValidOnesApplied(t *testing.T) {
	writeConfig(t, "bogus_key = 1\nmax_show = 6\n")
	if c := Load(); c.MaxShow != 6 {
		t.Errorf("valid key beside unknown one not applied: %d", c.MaxShow)
	}
}

// The file may redirect everything else, but never its own directory: locating
// it through its own contents would make the effective config depend on read
// order. It is found via XDG_CONFIG_HOME, then config_dir applies onward.
func TestFileCannotRelocateOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "gotomux"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "config_dir = \"/tmp/zz-relocated\"\nmax_show = 6\n"
	if err := os.WriteFile(filepath.Join(dir, "gotomux", "config.toml"),
		[]byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Load()
	if c.MaxShow != 6 {
		t.Fatalf("file not discovered via XDG_CONFIG_HOME")
	}
	if got, want := c.ResolveConfigDir(), "/tmp/zz-relocated/gotomux"; got != want {
		t.Errorf("ResolveConfigDir = %q, want %q (file's config_dir applies after discovery)",
			got, want)
	}
}

// TestSocketPathFollowsXDG pins the fix for the socket being derived from
// XDG_DATA_HOME in three places that all ignored DataDir: moving the data dir
// moved the store but not the socket, splitting client and daemon onto
// different state.
func TestSocketPathFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "/tmp/zz-xdg")
	c := Load()
	if got, want := c.SocketPath(), "/tmp/zz-xdg/gotomux/gotomux.sock"; got != want {
		t.Errorf("SocketPath = %q, want %q", got, want)
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

func TestNormalizeRepairsJunkEnums(t *testing.T) {
	c := &Config{Icons: "wat", Prewarm: "wat"}
	c.normalize()
	if c.Icons != "auto" || c.Prewarm != "auto" {
		t.Errorf("junk enum values not normalized: icons=%q prewarm=%q", c.Icons, c.Prewarm)
	}
}
