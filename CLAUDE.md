# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Go CLI (`gotomux`, module `github.com/fm39hz/gotomux`) for picking/creating tmux sessions and restoring saved layouts (tmuxp-like). Interactive fzf-style combobox via Bubble Tea; presets in SQLite.

## Commands

```bash
make build          # ./gotomux
make run            # picker; ARGS='-f' / '-e name' / '-h'
make test           # go test ./...
make install        # $(go env GOPATH)/bin
make help           # all targets
make pkg            # Arch package → dist/*.pkg.tar.zst

# raw go still fine:
go build -o gotomux .
go run . -f
```

No linter config or CI.

## Architecture

```
main.go                CLI entry (flags, connect, freeze/edit)
internal/
  project/            project root walk + session name sanitize
  store/              SQLite presets, usage, pairs, zox cache rows
  tmux/               local tmux ctl (list/freeze/load/attach) + pane detect
  template/           sticky templates + preset JSON format/parse/edit
  picker/             Bubble Tea UI, Source registry, rank, zoxide source
```

### Sources (picker)

`Source` = paint `Snapshot` + optional bg `Refresh`. Order (dedup first-wins): create → tmux → preset → zoxide.
Add remote later: implement `Source`, register in `defaultSources`, connect by `Item.Src`/`Host`.

### Data flow

1. **Paint**: each Source.Snapshot (zoxide = DB/mem cache).
2. **Refresh**: zoxide full `query -l` in background → merge pool (empty query still caps top 40).
3. **Connect**: create/zoxide → template bake; active → attach; preset → Load+attach.
4. **Freeze**: live → Freeze → Store.Save.
5. **Load** mirrors tmuxp: new-session/window/split; pin names; select-layout.

### Preset model

```
Preset { Name, Cwd, Windows[] }
  PresetWindow { Idx, Name, Cwd, Layout, Panes[] }
    PresetPane { Idx, Cwd, Cmd }  // Cmd empty = default shell
```

SQLite: `$XDG_DATA_HOME/gotomux/state.db` (default `~/.local/share/gotomux/state.db`). Cascade deletes; soft-migrate adds `window.cwd` if missing.

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

`project.FindProjectRoot` walks up for `project.godot`, `.git`, `package.json`, `Cargo.toml`, `go.mod`. `project.SessionName` sanitizes basename to `[a-z0-9-]`.

### External deps

- `gotmux` — session list/attach/switch/kill
- `bubbletea` + `lipgloss` — TUI
- `modernc.org/sqlite` — pure-Go sqlite
- Runtime: `tmux` required; `zoxide query -l` optional; `/proc` for freeze cmd detection (Linux)

### Connect behavior

Inside tmux (`$TMUX` set): `SwitchClient`. Outside: `Attach`. `Load` is no-op if session already exists.
