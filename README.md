# gotomux

[![CI](https://img.shields.io/github/actions/workflow/status/fm39hz/gotomux/ci.yml?branch=master&style=flat-square)](https://github.com/fm39hz/gotomux/actions)
[![Release](https://img.shields.io/github/v/release/fm39hz/gotomux?style=flat-square)](https://github.com/fm39hz/gotomux/releases)
[![AUR](https://img.shields.io/aur/version/gotomux?style=flat-square)](https://aur.archlinux.org/packages/gotomux)
[![Go](https://img.shields.io/github/go-mod/go-version/fm39hz/gotomux?style=flat-square)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)](LICENSE)

**go to mux**, (yet) another fuzzy tmux session picker with presets, shapes and zoxide.

Forget about tmux plumbing and just jump into work. One keystroke opens a fuzzy list of everything you could want to attach to:

- **Live sessions**, ranked by real usage, not just mru
- **Saved presets**, frozen snapshots of whole sessions (paths + commands)
- **Sticky shapes**, the topology of your cockpit (windows / panes / tools), re-baked into new projects via Create / Zoxide
- **Zoxide paths**, create a fresh session anywhere you've `z`'d before
- **Self-ranking**, the daemon learns co-occurrence *and* directed transitions from real attach diffs, so the item you want surfaces first

One static binary per platform. No accounts, no telemetry out, the only "cloud" is a local SQLite store.

## Why not a tmux script + fzf?

|                         | `fzf-tmux` one-liner | gotomux                    |
| :---------------------- | :------------------- | :------------------------- |
| Session list            | ✓                    | ✓ + usage-ranked           |
| Presets (frozen sessions) | ✗                  | ✓ (`gotomux -f`)           |
| Rebuild from shapes     | ✗                    | ✓ (sticky + learning)      |
| Zoxide as source        | ✗                    | ✓                          |
| Cold start              | —                    | ~5 ms (daemon: ~13 ms warm) |

## Quick start

```bash
# Arch
paru -S gotomux
systemctl --user enable --now gotomuxd   # optional, instant cold start

# any platform with Go
go install github.com/fm39hz/gotomux@latest
go install github.com/fm39hz/gotomux/cmd/gotomuxd@latest
```

Then bind it to a key (see [Shell setup](#shell-setup)) and press it. Done.

**Requires:** `tmux`. **Optional:** `zoxide`, a Nerd Font (`icons = "ascii"` in the config otherwise).

## Install

**Arch Linux**

```bash
paru -S gotomux
# optional, for instant cold start
systemctl --user enable --now gotomuxd
```

**Go (any platform)**

```bash
go install github.com/fm39hz/gotomux@latest
# binary: $(go env GOPATH)/bin/gotomux
# optional daemon:
go install github.com/fm39hz/gotomux/cmd/gotomuxd@latest
```

**From source**

```bash
git clone https://github.com/fm39hz/gotomux.git && cd gotomux
make install           # go install CLI
make install-all       # CLI + daemon + systemd unit + enable
# or: make build && ./gotomux
# or: make pkg         # Arch: dist/*.pkg.tar.zst
# or: make run         # picker, ARGS="-f" / "-e name" / "-p"
```

## Usage

```text
Usage: gotomux [flags]

Flags:
  -h, --help     Show this help
  -v, --version  Show version
  -f, --freeze   Freeze current or named session as a preset
  -e, --edit     Edit a named preset (or freeze-then-edit)
  -r, --reset    Restore current or named session to its baseline layout
  -p, --profile  Profile cold-start performance
```

The picker opens instantly. Type to filter, Enter to connect.

### Daemon (`gotomuxd`)

Optional background service: pre-warms the data and does all ranking telemetry, so the picker does zero fs / tmux I/O on its hot path.

```bash
systemctl --user enable --now gotomuxd
```

gotomux auto-detects the daemon. If absent it falls back to standalone mode, behaviorally identical, slightly slower cold start.

## Shell setup

This is the setup I use for myself, adapt it into your own shell config if needed.

**Nushell**

```nu
$env.config.keybindings ++= [{
  name: launch_gotomux
  modifier: control
  keycode: char_b
  mode: [emacs, vi_normal, vi_insert]
  event: { send: executehostcommand, cmd: "gotomux" }
}]
```

**Fish**

```fish
function fish_user_key_bindings
    for mode in insert default visual
        bind -M $mode \cb 'gotomux; commandline -f repaint'
    end
end
```

> [!TIP]
> **Tmux popup**: gotomux is a normal TUI, so it composes perfectly with `display-popup`.

```tmux
bind-key C-b display-popup -T " Go to mux " -w 80% -h 70% -x C -y C -E "$HOME/go/bin/gotomux"
bind-key C-e display-popup  -T " Edit config " -w 80% -h 90% -x C -y C -E "$HOME/go/bin/gotomux -e"
bind-key C-r run-shell "tmux display-message \"$($HOME/go/bin/gotomux -r)\""
bind-key -n C-f run-shell "tmux display-message \"$($HOME/go/bin/gotomux -f)\""
```

## Keybindings

### Picker

| Key                                | Action                            |
| ---------------------------------- | --------------------------------- |
| Enter                   | connect                           |
| Ctrl + N / Ctrl + P | next / prev         |
| Ctrl + U / Ctrl + W | clear query / delete word |
| Ctrl + X     | kill active session               |
| Ctrl + F     | freeze into preset + shape        |
| Ctrl + T     | set sticky shape for new projects |
| Ctrl + E / Ctrl + D | edit / delete preset |
| Esc / Ctrl + C | cancel                     |
| ?                       | toggle help                       |

## Behaviour

| Item                | Enter                                                                               |
| ------------------- | ----------------------------------------------------------------------------------- |
| **Active**          | attach / switch                                                                     |
| **Preset**          | load if missing, then attach                                                        |
| **Create / Zoxide** | live? attach : same-name preset? load : unfreeze **sticky shape** into project root |

## Shapes

A **shape** is cockpit essence (no paths, no pixel dumps).
Freeze saves a full instance, and a shape is derived from it (topology + tools only).
Sticky shapes are used for new projects via Create / Zoxide.

```json
{
  "id": "shape-2942bbbd21e65a14",
  "label": "nvim+v2+yazi",
  "windows": [
    { "fork": "1||nvim", "name": "editor", "panes": [{ "cmd": "nvim" }] },
    {
      "fork": "2|even-vertical|",
      "name": "shell",
      "split": "even-vertical",
      "panes": [{}, {}]
    },
    { "fork": "1||yazi", "name": "files", "panes": [{ "cmd": "yazi" }] }
  ]
}
```

The `fork` string is a window essence fingerprint (`panes|split|tools`).
Common patterns accumulate hit counts in the DB and can be composed into new shapes automatically.
Shapes live as editable JSON next to your config: `$XDG_CONFIG_HOME/gotomux/shapes/<label>--<id8>.json`.

## Reset

`gotomux -r` restores a session to its recorded baseline layout, run it inside tmux for the current session, or pass a name (`gotomux -r my-session`).

- **Baseline**: the preset written when the session was created (bake) or frozen. Hand-built sessions get one on the first `-r`, so a second run actually restores.
- **Dead windows** (the pane exited, tmux closed the window, renumbering shifted the rest) are recreated at their baseline index with their original command; surviving windows are moved back to their baseline position.
- **Nothing is killed**: windows you added beyond the baseline stay, processes intact.

No output and no changes when the session already matches the baseline.

## Ranking

Sources form a space × time matrix:

|         | Here       | Anywhere   |
| ------- | ---------- | ---------- |
| Future  | **Create** | **Zoxide** |
| Present | —          | **Active** |
| Past    | —          | **Preset** |

Sort: `tier > recency > cooccur > trans > kind > detail > busy > pathQ > idx`.
Same formula everywhere, environment only changes inputs:

- **Inside tmux** (`ctxSession` set): items matching the current session name or path are excluded; co-occurrence (what you keep open alongside) and directed transition (what you usually switch *into*) overlays are active.
- **Outside tmux**: all items visible; co-occurrence / transitions = 0.

The daemon learns transitions from real attach diffs (`session_attached` count deltas between polls, exactly one session lost a client and exactly one gained one), so it sees native tmux switches too, not just gotomux ones.

"Just left" surfaces via recency.

## Configuration

Settings live in `$XDG_CONFIG_HOME/gotomux/config.toml`, next to `shapes/`.
Precedence: defaults < config file. The file is the single inspectable source, there is deliberately no environment layer for settings.

```toml
# gotomux configuration
data_dir        = ""     # base for state.db + socket   ($XDG_DATA_HOME/gotomux)
config_dir      = ""     # base for shapes/ + this file ($XDG_CONFIG_HOME/gotomux)

poll_interval   = "10s"  # daemon sync cadence
zoxide_cap      = 40     # zoxide rows when the query is empty
max_show        = 12     # visible picker rows
git_concurrency = 4      # git enrich workers
proc_cache_ttl  = "2s"   # pane process detection cache
prune_cutoff    = "720h" # stale row prune age

icons           = "auto" # auto | nerd | ascii

[daemon]
autostart = true         # picker may start gotomuxd
prewarm   = "auto"       # auto (rotational disks) | on | off
```

Unknown keys are reported and ignored; a malformed entry degrades to its default instead of failing startup.
The daemon reads the file at start, `systemctl --user restart gotomuxd` after edits.

### Data

| Path                                                  | Contents                                          |
| ----------------------------------------------------- | ------------------------------------------------- |
| `$XDG_CONFIG_HOME/gotomux/shapes/<label>--<id8>.json` | shape backup (auto-reconciled)                   |
| `$XDG_DATA_HOME/gotomux/state.db`                     | presets, shapes, usage, pairs, transitions, forks |
| `$XDG_DATA_HOME/gotomux/gotomux.sock`                 | daemon IPC (if running)                          |
| `$XDG_CONFIG_HOME/gotomux/config.toml`                | settings (defaults < file)                       |

## Development

```bash
make help    # every target
make test    # go test ./... (add -short for CI-style)
make bench   # picker cold-start benchmarks
make fmt vet # gofmt + go vet
make pkg     # Arch package -> dist/*.pkg.tar.zst
```

## Roadmap

Local first.

- [x] Sources: create / tmux / preset / zoxide
- [x] Freeze / load, sticky shapes, placement + fork learning
- [x] Shape labels, config reconcile, product JSON (`split` / tools)
- [x] `go install` / CI / local Arch package
- [x] AUR release
- [ ] Polish everyday use until boring
- [ ] Remote tmux as one pool (`tmux@host`; server: tmux + ssh only)

> [!WARNING]
> gotomux is still in early development. Some unintended behavior might occur.

## Acknowledgements

- [tmux](https://github.com/tmux/tmux): Obviously, what do you expect?
- [zoxide](https://github.com/ajeetdsouza/zoxide): Smart directory jump
- [Bubble Tea](https://github.com/charmbracelet/bubbletea): TUI library
- [fzf](https://github.com/junegunn/fzf): Fuzzy match core engine
- [modernc sqlite](https://gitlab.com/cznic/sqlite): Go version of SQLite
- [gopsutil](https://github.com/shirou/gopsutil): psutil for Go
- [projectdetect](https://github.com/richardwooding/projectdetect): Detect project type
- [go-devicons](https://github.com/epilande/go-devicons): Nerd font icon

## License

[MIT](LICENSE)
