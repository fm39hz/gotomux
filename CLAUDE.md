# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Go CLI (`tmux_project`) for picking/creating tmux sessions and restoring saved layouts (tmuxp-like). Interactive fzf-style combobox via Bubble Tea; presets in SQLite.

## Commands

```bash
go build -o tmux_project .   # binary (gitignored)
go run .                     # interactive picker
go run . -f                  # freeze active session → sqlite
go run . -e [name]           # edit preset in $EDITOR
go run . -h

go test ./...                # integration tests — need live tmux server
go test -run TestLoadGrimoireShape -v
go test -run TestLoadKhoCongShape -v
```

No Makefile, linter config, or CI. Tests spawn real sessions named `tp-test-*` and kill them; they fail if `tmux` is missing or the server is unreachable.

## Architecture

Single `package main`. No subpackages.

| File | Role |
|------|------|
| `main.go` | CLI flags, `run()` / `connectItem()`, freeze/edit entry |
| `ui.go` | Main Bubble Tea picker: item kinds, fuzzy filter, keybinds, in-TUI edit via `tea.ExecProcess` |
| `pick.go` | Smaller picker used by `-f` (session name only) |
| `tmuxctl.go` | `TmuxCtl` — list/kill/attach via gotmux; `Freeze` / `Load` via raw `tmux` commands |
| `detect.go` | Pane cmd detection: `pane_current_command` else `/proc` child walk (Linux) |
| `store.go` | SQLite schema + CRUD for presets (`session` / `window` / `pane`) |
| `edit.go` | Human text format ↔ `Preset`; `$EDITOR` workflow for `-e` |

### Data flow

1. **Picker items** (`collectItems`): `[Create]` from cwd project root → live tmux sessions → sqlite presets → zoxide paths. Dedup by session name.
2. **Connect**: create/active/zoxide → empty or attach session; preset → `Load` (if missing) then attach/switch.
3. **Freeze**: live session → `Freeze` (windows/panes/cwd/cmd) → `Store.Save`.
4. **Load** mirrors tmuxp: `new-session` / `new-window` / `split-window -h` with optional cmd arg; pins window names (`automatic-rename off`); restores layout via `select-layout`.

### Preset model

```
Preset { Name, Cwd, Windows[] }
  PresetWindow { Idx, Name, Cwd, Layout, Panes[] }
    PresetPane { Idx, Cwd, Cmd }  // Cmd empty = default shell
```

SQLite: `$XDG_DATA_HOME/tmux_project/state.db` (default `~/.local/share/tmux_project/state.db`). Cascade deletes; soft-migrate adds `window.cwd` if missing.

### Edit text format

```
name: my-session
cwd: /path

[window: editor]
layout: even-horizontal   # optional
pane: /path | nvim
pane: /path |
```

### Project root / session naming

`findProjectRoot` walks up for `project.godot`, `.git`, `package.json`, `Cargo.toml`, `go.mod`. `sessionName` sanitizes basename to `[a-z0-9-]`.

### External deps

- `gotmux` — session list/attach/switch/kill
- `bubbletea` + `lipgloss` — TUI
- `modernc.org/sqlite` — pure-Go sqlite
- Runtime: `tmux` required; `zoxide query -l` optional; `/proc` for freeze cmd detection (Linux)

### Connect behavior

Inside tmux (`$TMUX` set): `SwitchClient`. Outside: `Attach`. `Load` is no-op if session already exists.
