# gotomux

[![CI](https://img.shields.io/github/actions/workflow/status/fm39hz/gotomux/ci.yml?branch=master&style=flat-square)](https://github.com/fm39hz/gotomux/actions)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)](LICENSE)

**go to mux**, (yet) another fuzzy tmux session picker with presets, shapes, zoxide.

Forget about tmux and just jump into work: live sessions, saved presets, with [zoxide](https://github.com/ajeetdsouza/zoxide) paths, and **sticky shapes** reapplied on new projects.

## Install

**Arch Linux:**

```bash
yay -S gotomux                            # or paru, pamac,...
systemctl --user enable --now gotomuxd    # daemon for instant cold start
```

**Go (any platform):**

```bash
go install github.com/fm39hz/gotomux@latest
# binary: $(go env GOPATH)/bin/gotomux
# optional daemon:
go install github.com/fm39hz/gotomux/cmd/gotomuxd@latest
```

**From source:**

```bash
git clone https://github.com/fm39hz/gotomux.git && cd gotomux
make install           # go install CLI
make install-all       # CLI + daemon + systemd unit + enable
# or: make build && ./gotomux
# or: make pkg         # Arch: dist/*.pkg.tar.zst
```

**Requires:** `tmux`, `zoxide` (optional)
**Optional:** Nerd Font (icons, or `GOTOMUX_ASCII=1`)

## Usage

```bash
Usage: gotomux [flags]

Flags:
  -h, --help     Show this help
  -v, --version  Show version
  -f, --freeze   Freeze current or named session as a preset
  -e, --edit     Edit a named preset (or freeze-then-edit)
  -p, --profile  Profile cold-start
```

The picker opens instantly. Type to filter, Enter to connect.

### Daemon (`gotomuxd`)

Optional background service for faster cold start and automatic reranking supported telemetry.

```bash
systemctl --user enable --now gotomuxd
```

Gotomux auto-detects the daemon on startup. If absent, it will falls back to standalone mode (works identically, slightly slower cold start).

## Keybind

### Picker

| Key                 | Action                            |
| ------------------- | --------------------------------- |
| type                | filter (diacritics folded)        |
| `enter`             | connect                           |
| `ctrl-n` / `ctrl-p` | next / prev                       |
| `ctrl-u` / `ctrl-w` | clear query / delete word         |
| `ctrl-x`            | kill active session               |
| `ctrl-f`            | freeze into preset + shape        |
| `ctrl-t`            | set sticky shape for new projects |
| `ctrl-e` / `ctrl-d` | edit / delete preset              |
| `esc` / `ctrl-c`    | cancel                            |
| `?`                 | toggle help                       |

### Shell

This is the setup i use for myself, adapt into your own shell config if needed

```bash
# nushell
$env.config.keybindings ++= [{
  name: launch_gotomux
  modifier: control
  keycode: char_b
  mode: [emacs, vi_normal, vi_insert]
  event: { send: executehostcommand, cmd: "gotomux" }
}]
```

### Tmux popup

```tmux
bind-key C-b display-popup -T " Go to mux " -w 80% -h 70% -x C -y C -E "$HOME/go/bin/gotomux"
bind-key C-e run-shell "$HOME/go/bin/gotomux -e"
bind-key -n C-f run-shell "tmux display-message \"$($HOME/go/bin/gotomux -f)\""
```

## Behaviour

| Item                | Enter                                                                               |
| ------------------- | ----------------------------------------------------------------------------------- |
| **Active**          | attach / switch                                                                     |
| **Preset**          | load if missing, then attach                                                        |
| **Create / Zoxide** | live? attach : same-name preset? load : unfreeze **sticky shape** into project root |

### Shapes

A **shape** is cockpit essence (no paths, no pixel dumps).
Freeze saves a full instance, and a shape is derived from it (topology + tools only).
Sticky shapes are used for new projects via Create/Zoxide.

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

### Data

| Path                                                  | Contents                       |
| ----------------------------------------------------- | ------------------------------ |
| `$XDG_CONFIG_HOME/gotomux/shapes/<label>--<id8>.json` | shape backup (auto-reconciled) |
| `$XDG_DATA_HOME/gotomux/state.db`                     | presets, shapes, usage, forks  |
| `$XDG_DATA_HOME/gotomux/gotomuxd.sock`                | daemon IPC (if running)        |

## Ranking

Sources is form into a space × time matrix ranking

|         | Here       | Anywhere   |
| ------- | ---------- | ---------- |
| Future  | **Create** | **Zoxide** |
| Present | —          | **Active** |
| Past    | —          | **Preset** |

Sort: `tier > recency > cooccur > kind > detail > busy > pathQ > idx`.
Same formula everywhere, environment only changes inputs:

- **Inside tmux** (`ctxSession` set): items matching the current session name or path are excluded; co-occurrence overlay active.
- **Outside tmux**: all items visible; co-occurrence = 0.

"Just left" surfaces via recency.

## Env

| Variable            | Effect                        |
| ------------------- | ----------------------------- |
| `GOTOMUX_ASCII=1`   | TUI without Nerd Font icons   |
| `GOTOMUX_NERD=1`    | force nerd icons              |
| `EDITOR` / `VISUAL` | preset edit (`-e` / `ctrl-e`) |

## Roadmap

Local first.
⚠️ WARNING: gotomux is still in early development stages. Some unintended behavior might occur.
If you encounter any issue, please report it so I may fix it.

- [x] Sources: create / tmux / preset / zoxide
- [x] Freeze / load, sticky shapes, placement + fork learning
- [x] Shape labels, config reconcile, product JSON (`split` / tools)
- [x] `go install` / CI / local Arch package
- [x] AUR release
- [ ] Polish everyday use until boring
- [ ] Remote tmux as one pool (`tmux@host`; server: tmux + ssh only)

## Dev

```bash
make help && make test
```

## Acknowledgements

[tmux](https://github.com/tmux/tmux): Obviously, what do you expect?
[zoxide](https://github.com/ajeetdsouza/zoxide): Smart directory jump
[Bubble Tea](https://github.com/charmbracelet/bubbletea): TUI library
[fzf](https://github.com/junegunn/fzf): Fuzzy match core engine
[modernc sqlite](https://gitlab.com/cznic/sqlite): Go version of Sqlite
[gopsutil](https://github.com/shirou/gopsutil): psutil for Go
[projectdetect](https://github.com/richardwooding/projectdetect): Detect project type
[go-devicons](https://github.com/epilande/go-devicons): Nerd font icon

## License

[MIT](LICENSE)
