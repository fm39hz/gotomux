# gotomux

[![CI](https://img.shields.io/github/actions/workflow/status/fm39hz/gotomux/ci.yml?branch=master&style=flat-square)](https://github.com/fm39hz/gotomux/actions)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)](LICENSE)

**go to mux** - (yet) another fuzzy [tmux](https://github.com/tmux/tmux) session picker.

Forget about tmux and just jump into work: live sessions, saved presets, optional [zoxide](https://github.com/ajeetdsouza/zoxide) paths, and **sticky shapes** (cockpit layout + tools) reapplied on new projects.

## Install

```bash
go install github.com/fm39hz/gotomux@latest
# binary: $(go env GOPATH)/bin/gotomux
```

From source:

```bash
git clone https://github.com/fm39hz/gotomux.git && cd gotomux
make install    # or: make build && ./gotomux
make pkg        # Arch: dist/*.pkg.tar.zst
```

**Requires:** `tmux`, `zoxide`
**Optional:** Nerd Font (TUI icons, or set `GOTOMUX_ASCII=1` to forces plain text)

## CLI

```bash
gotomux             # interactive picker
gotomux -f [name]   # freeze session (arg, else current, else pick)
gotomux -e [name]   # edit preset in $EDITOR
gotomux -h          # show help
```

## Keybind

### picker

| Key                 | Action                                                |
| ------------------- | ----------------------------------------------------- |
| type                | filter (diacritics folded)                            |
| `enter`             | connect                                               |
| `ctrl-n` / `ctrl-p` | next / prev                                           |
| `ctrl-u` / `ctrl-w` | clear query / delete word                             |
| `ctrl-x`            | kill active session                                   |
| `ctrl-f`            | freeze selection -> preset + shape                    |
| `ctrl-t`            | sticky shape from selection (create/zox bake with it) |
| `ctrl-e` / `ctrl-d` | edit / delete preset                                  |
| `esc` / `ctrl-c`    | cancel (exit 0)                                       |
| `?`                 | toggle help                                           |

### shell (launch picker only)

One action: run `gotomux` (Ctrl+B in the samples; pick another chord if it clashes). Binary on `PATH` (e.g. `$(go env GOPATH)/bin`).

#### nushell

[`ExecuteHostCommand`](https://www.nushell.sh/book/line_editor.html) runs a command without stuffing the buffer/history. Append to `$env.config.keybindings` (see [line editor](https://www.nushell.sh/book/line_editor.html)):

```nu
$env.config.keybindings ++= [{
  name: launch_gotomux
  modifier: control
  keycode: char_b
  mode: [emacs, vi_normal, vi_insert]
  event: {
    send: executehostcommand
    cmd: "gotomux"
  }
}]
```

If a key does nothing, check with `keybindings listen`.

### tmux

Inside tmux, use a popup so the TUI has a real TTY (needed with many non-bash default shells):

```tmux
bind-key C-b display-popup -E -w 80% -h 70% "$HOME/go/bin/gotomux"
bind-key C-e display-popup -E -w 90% -h 90% "$HOME/go/bin/gotomux -e"
bind-key -n C-f run-shell "$HOME/go/bin/gotomux -f >/dev/null 2>&1; tmux display-message 'Froze #{session_name}'"
```

| Bind                 | Action                                         |
| -------------------- | ---------------------------------------------- |
| prefix + `C-b` popup | picker                                         |
| `C-f`                | freeze current session                         |
| prefix + `C-e` popup | edit preset (`-e` defaults to current session) |

Shell Ctrl+B and tmux prefix+C-b are separate: outside tmux the shell bind runs; inside tmux the popup bind is usually what you want.

## Behaviour

| Item                | Enter                                                                         |
| ------------------- | ----------------------------------------------------------------------------- |
| **Active**          | attach / switch                                                               |
| **Preset**          | load if missing, then attach                                                  |
| **Create / Zoxide** | live? attach : same-name preset? load : bake **sticky shape** at project root |

If connect fails (tmux error, broken preset,...), the picker re-opens with the error message in the status line.

### Shapes (sticky)

A **shape** is cockpit essence, not a project dump:

- window/pane counts, split class (`even-vertical`, `tiled`,...), tool intent (`nvim`, `yazi`, `opencode`, ...)
- **not** absolute paths, tmux pixel dumps, or session names
- human **label** (e.g. `nvim+v2+yazi`) with stable **id** inside the file
- umbrella layouts learn pane placement (R / C0 / C1 / ...) silently from freeze

Freeze remembers a session **instance** (full cwd/cmd for re-attach) and updates the shape library. Sticky only changes what Create/Zoxide bake.

### Data

| Path                                                  | Contents                                         |
| ----------------------------------------------------- | ------------------------------------------------ |
| `$XDG_DATA_HOME/gotomux/state.db`                     | presets, shapes, sticky, placement, forks, usage |
| `$XDG_CONFIG_HOME/gotomux/shapes/<label>--<id8>.json` | shape backup (auto-reconciled from DB)           |

Shape JSON sketch:

```json
{
  "id": "shape-2942bbbd21e65a14",
  "label": "nvim+v2+yazi",
  "windows": [
    { "name": "editor", "panes": [{ "cmd": "nvim" }] },
    { "name": "shell", "split": "even-vertical", "panes": [{}, {}] },
    { "name": "files", "panes": [{ "cmd": "yazi" }] }
  ]
}
```

## Ranking

Sources form a space×time matrix:

|         | Here       | Anywhere   |
| ------- | ---------- | ---------- |
| Future  | **Create** | **Zoxide** |
| Present | -          | **Active** |
| Past    | -          | **Preset** |

Tiered tuple sort: `tier > recency > cooccur > kind > detail > busy > pathQ > idx`.
Same algorithm everywhere - environment (terminal, tmux, remote) only changes inputs:

- **ctxSession set** (inside tmux): items matching current session name excluded;
  cooccur overlay active for remaining items.
- **No ctxSession** (terminal, remote standalone): all items visible; cooccur = 0.

"Just left" surfaces via recency - no special case.

## Env

| Variable            | Effect                        |
| ------------------- | ----------------------------- |
| `GOTOMUX_ASCII=1`   | TUI without Nerd Font icons   |
| `GOTOMUX_NERD=1`    | force nerd icons              |
| `EDITOR` / `VISUAL` | preset edit (`-e` / `ctrl-e`) |

## Roadmap

Local first.

- [x] Sources: create / tmux / preset / zoxide
- [x] Freeze / load, sticky shapes, placement + fork learning
- [x] Shape labels, config reconcile, product JSON (`split` / tools)
- [x] `go install` / CI / local Arch package
- [ ] Polish everyday use until boring
- [ ] Remote tmux as one pool (`tmux@host`; server: tmux + ssh only)
- [ ] AUR

## Dev

```bash
make help && make test
```

## Acknowledgements

- [tmux](https://github.com/tmux/tmux), [zoxide](https://github.com/ajeetdsouza/zoxide)
- [Bubble Tea](https://github.com/charmbracelet/bubbletea), [fzf](https://github.com/junegunn/fzf), [gotmux](https://github.com/GianlucaP106/gotmux), [modernc sqlite](https://gitlab.com/cznic/sqlite)
- [gopsutil](https://github.com/shirou/gopsutil), [projectdetect](https://github.com/richardwooding/projectdetect), [go-devicons](https://github.com/epilande/go-devicons)

## License

[MIT](LICENSE)
