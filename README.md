# gotomux

**go to mux**, a fuzzy tmux session picker with presets, zoxide and sticky templates.

Create / attach sessions, freeze live layouts to SQLite, bake a default (or sticky) template when opening a new project path. Built so you stop thinking about tmux and just jump into work.

## Requirements

- [tmux](https://github.com/tmux/tmux)
- Optional: [zoxide](https://github.com/ajeetdsouza/zoxide) for frequent paths

## Install

Pick one. Easiest first.

### 1. `go install` (recommended if you have Go)

```bash
go install github.com/fm39hz/gotomux@latest
```

Binary lands in `$(go env GOPATH)/bin`, put that on `PATH`.

Pin a version

```bash
go install github.com/fm39hz/gotomux@v0.1.0
```

### 2. Release binary (no Go toolchain)

1. Open [Releases](https://github.com/fm39hz/gotomux/releases)
2. Grab `gotomux_<ver>_linux_amd64.tar.gz` (or darwin / arm64)
3. Extract and put `gotomux` on `PATH`

```bash
# example
tar -xzf gotomux_*_linux_amd64.tar.gz
sudo install -m755 gotomux /usr/local/bin/gotomux
gotomux -h
```

Checksums: `checksums.txt` on the same release.

### 3. Arch (local package)

```bash
git clone https://github.com/fm39hz/gotomux.git
cd gotomux
make pkg
sudo pacman -U dist/gotomux-*.pkg.tar.zst
```

AUR later.

### 4. From source

```bash
git clone https://github.com/fm39hz/gotomux.git
cd gotomux
make build    # → ./gotomux
# or: make install
```

## Usage

```bash
gotomux           # picker
gotomux -f        # freeze current session (or pick if outside tmux)
gotomux -e [name] # edit preset JSON in $EDITOR
gotomux -h
```

### Keys

| Key                 | Action                                             |
| ------------------- | -------------------------------------------------- |
| type                | filter (anytime)                                   |
| `enter`             | connect                                            |
| `ctrl-n` / `ctrl-p` | next / prev                                        |
| `ctrl-x`            | kill active session                                |
| `ctrl-f`            | freeze active → preset                             |
| `ctrl-e`            | edit preset                                        |
| `ctrl-d`            | delete preset                                      |
| `ctrl-t`            | sticky template from preset (again: reset default) |
| `?`                 | toggle full key help                               |
| `esc`               | quit                                               |

### Connect rules

| Item                | Behavior                                                      |
| ------------------- | ------------------------------------------------------------- |
| **Active**          | switch / attach                                               |
| **Preset**          | load layout if missing, then attach                           |
| **Create / Zoxide** | live → same-name preset → **active template** at project root |

Default template (auto-seeded):

`$XDG_DATA_HOME/gotomux/templates/default.json`

Sticky name: `templates/active`.  
`ctrl-t` on a preset writes `templates/<name>.json` and sets sticky.

### Ranking (filter)

List order is **lexicographic**, not a single “magic score”. Better match tier always wins over kind or usage.

| Priority | Field     | Meaning                                                   |
| -------- | --------- | --------------------------------------------------------- |
| 1        | `tier`    | match quality (exact → prefix → substr → fuzzy → path)    |
| 2        | `kind`    | Create > Active > Preset > Zoxide                         |
| 3        | `detail`  | density / position within tier                            |
| 4        | `recency` | app frecency (opens/kills/time) or preset/zoxide fallback |
| 5        | `cooccur` | sessions often used together with current session         |
| 6        | `pathQ`   | shallower path preferred                                  |

Empty query: kind → recency → cooccur. Typed multi-token queries are AND.

### Freeze / load

`-f` or `ctrl-f` snapshots windows/panes (cwd + detected cmd) into SQLite.

Load rebuilds with `new-session` / `new-window` / `split-window`, pins window names, restores layout (`select-layout`).

### Edit format

`gotomux -e` / `ctrl-e` opens pretty JSON:

```json
{
  "name": "my-session",
  "cwd": "/path",
  "windows": [
    {
      "name": "editor",
      "layout": "even-horizontal",
      "panes": [{ "cwd": "/path", "cmd": "nvim" }, { "cwd": "/path" }]
    }
  ]
}
```

`layout`: named (`even-horizontal`, …) or a tmux `window_layout` dump from freeze.

### Store

`$XDG_DATA_HOME/gotomux/state.db` (default `~/.local/share/gotomux/state.db`).

## Dev

```bash
make help
make test
make pkg          # Arch package in dist/
```

## License

[MIT](LICENSE)
