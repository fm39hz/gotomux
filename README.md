# Tmux Project

A small tools created after i'm so tired with manually manage tmuxp and sesh setup

Interactive tmux session picker with saved layouts (tmuxp-like), fuzzy filter, and project templates.

Create / attach sessions, freeze live layouts to SQLite, bake a default (or sticky) template when opening a new project path.

## Requirements

- [tmux](https://github.com/tmux/tmux)
- Optional: [zoxide](https://github.com/ajeetdsouza/zoxide) for frequent paths

## Install

```bash
go install github.com/fm39hz/tmux_project@latest
```

Binary lands in `$(go env GOPATH)/bin` (or `GOBIN`). Ensure that directory is on `PATH`.

### Arch (AUR)

PKGBUILD later — for now:

```bash
git clone https://github.com/fm39hz/tmux_project.git
cd tmux_project
go build -ldflags='-s -w' -o tmux_project .
# install -Dm755 tmux_project /usr/bin/tmux_project
```

## Usage

```bash
tmux_project           # picker
tmux_project -f        # freeze active session → sqlite
tmux_project -e [name] # edit preset JSON in $EDITOR
tmux_project -h
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

| Item                | Behavior                                                   |
| ------------------- | ---------------------------------------------------------- |
| **Active**          | switch / attach                                            |
| **Preset**          | load layout if missing, then attach                        |
| **Create / Zoxide** | live → same-name preset → **active template** at that path |

Default template (auto-seeded):

`~/.local/share/tmux_project/templates/default.json`

Sticky name: `templates/active`.  
`ctrl-t` on a preset writes `templates/<name>.json` and sets sticky.

### Preset JSON

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

`layout`: named only (`even-horizontal`, `even-vertical`, `main-horizontal`, `main-vertical`, `tiled`). Multi-pane with empty layout → equal `even-horizontal` on bake.

### Data

```
$XDG_DATA_HOME/tmux_project/   # default ~/.local/share/tmux_project
  state.db
  templates/
    default.json
    active
```

## Build

```bash
go build -ldflags='-s -w' -o tmux_project .
go test ./...   # needs a live tmux server for integration tests
```

## License

[MIT](LICENSE)
