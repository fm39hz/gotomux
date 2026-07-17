# gotomux

**go to mux**, a fuzzy tmux session picker with presets, zoxide and sticky templates.

Create / attach sessions, freeze live layouts to SQLite, bake a default (or sticky) template when opening a new project path. Built so you stop thinking about tmux and just jump into work.

## Requirements

- [tmux](https://github.com/tmux/tmux)
- Optional: [zoxide](https://github.com/ajeetdsouza/zoxide) for frequent paths

## Install

```bash
go install github.com/fm39hz/gotomux@latest
```

Binary: `$(go env GOPATH)/bin/gotomux` (keep that dir on `PATH`).

### From source

```bash
git clone https://github.com/fm39hz/gotomux.git
cd gotomux
go build -ldflags='-s -w' -o gotomux .
```

### Arch (AUR)

Later — for now build from source as above.

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

`layout`: named (`even-horizontal`, …) or a tmux `window_layout` dump from freeze.

### Data

```
$XDG_DATA_HOME/gotomux/      # default ~/.local/share/gotomux
  state.db                   # also accepts legacy .../go-tomux or .../tmux_project if present
  templates/
    default.json
    active
```

### tmux bind example

```tmux
bind-key -n M-b display-popup -E -w 80% -h 70% "$HOME/go/bin/gotomux"
bind-key -n C-f run-shell "$HOME/go/bin/gotomux -f >/dev/null 2>&1; tmux display-message 'froze #{session_name}'"
```

## Build / test

```bash
go build -ldflags='-s -w' -o gotomux .
go test ./...   # integration tests need a live tmux server
```

## License

[MIT](LICENSE)
