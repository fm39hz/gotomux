# gotomux

[![CI](https://img.shields.io/github/actions/workflow/status/fm39hz/gotomux/ci.yml?branch=master&style=flat-square)](https://github.com/fm39hz/gotomux/actions)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)](LICENSE)

**go to mux**, (yet) another fuzzy [tmux](https://github.com/tmux/tmux) session picker: live sessions, SQLite presets, optional [zoxide](https://github.com/ajeetdsouza/zoxide) paths, sticky templates, built so you stop thinking about tmux and just jump into work.

## Install

### Go

```bash
go install github.com/fm39hz/gotomux@latest
# binary: $(go env GOPATH)/bin/gotomux
```

### Build from source

```bash
git clone https://github.com/fm39hz/gotomux.git && cd gotomux
make install   # or: make build && ./gotomux
make pkg       # Arch: dist/*.pkg.tar.zst
```

**Needs:** `tmux`. **Optional:** `zoxide` (`zoxide query -l`).

## tmux

```tmux
# adjust path if needed
bind-key C-b display-popup -E -w 80% -h 70% "$HOME/go/bin/gotomux"
bind-key C-e display-popup -E -w 90% -h 90% "$HOME/go/bin/gotomux -e"
bind-key -n C-f run-shell "$HOME/go/bin/gotomux -f >/dev/null 2>&1; tmux display-message 'Froze #{session_name}'"
```

`tmux source-file` your config.  
`C-b` = picker, `C-f` = freeze, `C-e` = edit current session preset (`-e` defaults to `#{session_name}` inside tmux).

CLI:

```bash
gotomux              # picker
gotomux -f           # freeze
gotomux -e [name]    # edit preset in $EDITOR
gotomux -h
```

### Keys

| Key                 | Action                      |
| ------------------- | --------------------------- |
| type                | filter (diacritics folded)  |
| `enter`             | connect                     |
| `ctrl-n` / `ctrl-p` | next / prev                 |
| `ctrl-x`            | kill active                 |
| `ctrl-f`            | freeze → preset             |
| `ctrl-e` / `ctrl-d` | edit / delete preset        |
| `ctrl-t`            | sticky shape from selection |
| `?` / `esc`         | help / quit                 |

### Behaviour

| Item            | Enter                                                 |
| --------------- | ----------------------------------------------------- |
| Active          | attach / switch                                       |
| Preset          | load if missing, then attach                          |
| Create / Zoxide | live → same-name preset → sticky shape @ project root |

State: `$XDG_DATA_HOME/gotomux/state.db` (presets · shapes · sticky · usage)  
Layouts: `$XDG_CONFIG_HOME/gotomux/layouts/<id>.json`, 1-1 reflect with DB (backup / git / hand-edit)

## Roadmap

Local first.

- [x] Picker sources: create · tmux · preset · zoxide
- [x] Freeze / load, sticky templates, rank + accent fold
- [x] `go install` / CI / local Arch package
- [ ] Remote tmux as one pool (`tmux@host`; server: tmux + ssh only)
- [ ] AUR publish

## Dev

```bash
make help && make test
```

## Acknowledgements

- [tmux](https://github.com/tmux/tmux)
- [zoxide](https://github.com/ajeetdsouza/zoxide)

Also: [Bubble Tea](https://github.com/charmbracelet/bubbletea), [gotmux](https://github.com/GianlucaP106/gotmux), [modernc sqlite](https://gitlab.com/cznic/sqlite).

## License

[MIT](LICENSE)
