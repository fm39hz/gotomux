# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Go CLI (`gotomux`, module `github.com/fm39hz/gotomux`) for picking/creating tmux sessions from live sessions, saved presets, and zoxide paths. Interactive fzf-style combobox via Bubble Tea; state in SQLite. Ships a second binary, `gotomuxd`, an optional user daemon that pre-warms the picker's data.

## Commands

```bash
make build          # ./gotomux
make build-all      # + ./gotomuxd
make run            # picker; ARGS='-f' / '-e name' / '-p'
make test           # go test ./...
make bench          # go test ./internal/picker/ -bench=. -benchmem
make fmt vet        # gofmt -w . ; go vet ./...
make install-all    # CLI + daemon + systemd user unit (enables gotomuxd)
make pkg            # Arch package -> dist/*.pkg.tar.zst
make publish patch  # scripts/bump-version.sh: tag & push
make help           # all targets
```

Single test / package:

```bash
go test ./internal/template/ -run TestShapeKey -v
go test ./... -count=1 -short      # what CI runs
```

Test gating (matters when a test "doesn't run"):

- `-short` skips live-tmux tests in `internal/tmux/load_test.go`, `internal/tmux/control_test.go` and `internal/daemon/daemon_test.go`. CI has no tmux server, so it always passes `-short`. Run without `-short` locally to exercise real `new-session`/`split-window` and the control-mode transport.
- Env-gated, off by default: `MIGRATE_USER=1` (`template/migrate_user_test.go`), `RECONCILE_USER=1` (`template/reconcile_test.go`) — these touch the *real* `~/.config/gotomux/shapes`. `STARTUP_BENCH=1` (`picker/startup_bench_test.go`).

**Any test that touches tmux must go through `tmuxtest.Isolate` (`internal/tmuxtest`). Never set `TMUX_TMPDIR` by hand and never call `kill-server` in a test.** Setting `TMUX_TMPDIR` is not sufficient isolation: if the directory does not exist, tmux **silently ignores it**, `start-server` still returns 0, and the test operates on the developer's default socket — so the cleanup `kill-server` destroys their live sessions with no error anywhere. This is not hypothetical; it happened while building the control-mode transport. `Isolate` creates the directory, then *proves* isolation by checking `#{socket_path}` is inside it, and registers teardown only after that check passes. Verified discriminator: with the dir present the socket is `$TMUX_TMPDIR/tmux-<uid>/default`; with it absent the socket is `/tmp/tmux-<uid>/default`.

CI (`.github/workflows/ci.yml`) runs `go vet` + `go test -short`. No linter beyond vet. Tags `v*` trigger release + AUR push.

## Architecture

```
main.go               CLI entry: flags, daemon probe, freeze/edit/profile paths
cmd/gotomuxd/         daemon entry (thin; all logic in internal/daemon)
internal/
  config/    env-driven Config + XDG dir resolution (every path goes through this)
  model/     shared Session/Window/Pane/Usage types — the lingua franca
  daemon/    poll loop, tmux control-socket cache, Unix-socket IPC
  event/     tiny in-process pub/sub (freeze.done, shape.saved)
  picker/    Bubble Tea UI, Source registry, ranking, zoxide, git enrich
  project/   project root walk, session-name sanitize, children, git
  store/     SQLite: presets, usage, pairs, shapes, placement, forks, zox cache
  template/  shape derive/bake/label, JSON format, config-dir mirror, freeze glue
  tmux/      Ctl (exec) + ControlConn (direct socket), freeze, load, pane detect
  toolclass/ hardcoded vocabulary: shell/editor/files/git/agent + chrome roles + icons
```

### Two run modes — this is the main branch point

`runPicker` dials `$XDG_DATA_HOME/gotomux/gotomux.sock` with a 50ms timeout.

- **Daemon present** → `runPickerIPC`: one `list` request returns sessions, presets, zoxide rows, pair scores, usage, and git branches already computed. The picker paints from that payload (`picker.NewModelFromDaemon`, `picker.PreloadCache`) with no filesystem or tmux I/O on the hot path. Connecting also sends a `connect` request so the daemon records telemetry.
- **No daemon** → `runPickerStandalone`: three goroutines in parallel (tmux ctl, store open, project root walk), then `picker.RunPicker`.

**Both paths must stay behaviorally identical.** Any new picker input needs a standalone source *and* a daemon cache field + `daemon.Response` field. IPC failures (bad response, 2s decode timeout) silently fall back to standalone — never make IPC a hard dependency.

Note: README documents the socket as `gotomuxd.sock`; the code uses `gotomux.sock` (`internal/daemon/daemon.go`, `handler.go`, `main.go`). The code is the truth.

### Daemon

`daemon.New` starts the tmux server (`start-server`, `exit-empty off`), dials tmux's own control socket directly via `tmux.StartControl` (no `tmux -C` subprocess), opens the store, then polls every `PollInterval` (10s). Each poll re-runs `ensureServer` / `ensureDB` (store `Ping` + reopen on failure) / `syncNow`.

- `syncNow` lists sessions in **one** control-socket round trip (`list-sessions` + `list-panes` combined, parsed by `tmux.ParseLiveOutput`), diffs against `lastSeen` to record open/pair telemetry, and refreshes the caches.
- `stateVersion` is an atomic counter; `list` requests carrying a matching `Version` get an empty 304-style response.
- Git branches are computed lazily on the first `list` (`ensureGitBranches`), not in `New` — cold disk I/O must not block startup.
- Single-instance is enforced twice: `flock` on `$XDG_RUNTIME_DIR/gotomux-<hash>.lock` plus stale-socket detection in `listenWithGuard`.

Perf is the point of this package. Recent commits are all deferral work (defer zoxide query, move git enrich into the daemon). When touching it, keep expensive work off `New()` and off the IPC response path.

#### tmux control mode — measured constraints (do not re-derive)

Verified on tmux 3.7b in throwaway servers (`tmux -L …`). These are not inferable from the code and each one killed a plausible design:

- **The daemon must never start the tmux server.** tmux registers a systemd *transient scope per pane*, parented under whatever unit started the server. A server started from `gotomuxd.service` therefore makes every pane of every session a child of that unit, and `systemctl --user stop gotomuxd` tears them all down — the journal logs `Stopping tmux child pane <pid>` for each and leaves the server alive with **zero sessions**. This destroyed real sessions during development. `KillMode=process` (now in `dist/gotomuxd.service`) saves the server *process* but not the pane scopes, so it is necessary and **not sufficient**. `daemon.New` therefore never runs `start-server`: it attaches only when `tmux.ServerRunning()` is already true, stays `Ready=false` otherwise (clients fall back to standalone), and `ensureControl` retries each poll. Correct topology, verified: `tmux-spawn-*.scope` units are *siblings* of `gotomuxd.service`, and the service cgroup holds only `gotomuxd` plus its own `tmux -C` client.
- **`exit-empty off` is no longer set.** It mutated the user's server globally and is redundant while the daemon owns a hidden session — the server never reaches zero sessions.
- **A control client must own a session, and attaching to a user session corrupts the data we serve.** `-C attach-session -t X` sets `session_attached=1` and bumps `session_last_attached` + `session_activity` on X. `-r` (read-only) does **not** help — it blocks input, not the attach. Since `LiveSession.Recency` is `max(LastAttached, Activity, Created)`, a daemon that attaches is falsifying its own ranking input.
- **The only non-perturbing invocation is a dedicated hidden session**: `tmux -C new-session -A -s __gotomuxd -- cat`. User sessions stay byte-identical (`attached=0`, `last_attached` still empty on never-attached sessions). `-- cat` avoids spawning a shell — with a real shell the stream floods with `%output` of the prompt. `-A` makes daemon restart reuse the session. The hidden session **is** visible to `list-sessions`, so every consumer must filter it.
- **`refresh-client -f no-output` suppresses `%output` entirely** (client gains the `no-output` flag; measured `%output` count drops to 0). Send it as the first command after connecting.
- **`buildCommand`-style quoting has two independent fatal bugs.** Quote set `" '\";"` omits TAB, so `ListSessFmt` (tab-separated) is split into argv by tmux and only `S` survives as the format. And `";"` *does* match the set, so the separator gets quoted to `';'` → `parse error: command list-sessions: too many arguments` → `%error` → **`%exit`**, killing the client. Correct approach: one command per line, single-quote every argument that is not a bare command/flag token, never quote the separator, and read one `%begin`/`%end` block per command. An unquoted `;` on one line does work but still yields **two** blocks.
- **Membership events**: creating a session emits `%sessions-changed` (plus `%unlinked-window-add`) — there is no `%session-created`. Killing emits `%sessions-changed` + `%unlinked-window-close`. Renaming emits `%session-renamed`. So `%sessions-changed` is the event to key membership resync on.
- **Events do not cover timestamps.** `session_activity` advances on any pane output with no event emitted, and it feeds `Recency`. Event-driven resync therefore does **not** replace the periodic poll — events handle membership, the poll handles timestamps. Keep both.
- **`%exit` means the client is gone** (e.g. after `%error` on a malformed command) — the transport must treat it as a reconnect trigger, not just a log line.
- `session_last_attached` is **empty** for never-attached sessions, so `ParseLiveOutput`'s numeric fields must tolerate `""` (`parseUnix` / discarded `Atoi` error already do).

### Shapes — the non-obvious core

A **preset** is a full session instance (paths, commands). A **shape** is its *essence*: window topology + pane count + split class + tool intent, with all paths and session-specific names stripped.

- `template.ToShape` derives a shape; `template.ShapeKey` hashes it (sha256, first 8 bytes hex) → id `shape-<key>`. Same essence ⇒ same id, so shapes dedupe naturally.
- `tmux.ToolIntent` reduces a pane command to a tool name; `toolclass` decides what counts as a shell (dropped, including `gotomux` itself) vs a real tool. Window names are re-derived into neutral chrome roles (`editor`/`shell`/`files`/`git`/`agent`) — a window named after the project gets renamed to its role.
- One shape is **sticky** (`sticky` table, single row). Creating a session from Create/Zoxide bakes the sticky shape into the project root (`template.ConnectProject` → `bakeShape`).
- **Placement** (`template/placement.go`) learns per-pane cwd slots as a pattern string — `R` = project root, `C0`/`C1` = `project.Children(root)[k]`, panes joined by `,`, windows by `|`. Learned only from freeze; all-root patterns are not worth recording. Bake uses `BestPlacement` when confident, else all-root.
- **Forks** (`template/fork.go`) learn window-level units keyed `paneCount|split|tools` (e.g. `2|even-vertical|nvim,`) with hit counters, so common window shapes can be recomposed.

Placement and fork learning are silent and never user-facing. They fire from `observeAfterShape`, which runs via the event bus on `freeze.done` (or inline if no bus is set — `cmd/gotomuxd` sets none, `main.go` does via `initEventBus`).

### Config-dir mirror

Every shape is mirrored to `$XDG_CONFIG_HOME/gotomux/shapes/<label>--<id8>.json` (`template/config.go`). `ensureShapesReady` syncs config→DB on read; `mirrorAfter` reconciles DB→config after writes. Hand-edited files are picked up. Legacy `layouts/` is auto-renamed to `shapes/`.

### Edit / shape file format is JSON

Documented in the header comment of `internal/template/edit.go` (`Format`/`Parse`). Product vocabulary only — never tmux dump strings, absolute paths, or percentage ratios in a pure shape:

```json
{
  "id": "shape-2942bbbd21e65a14",
  "label": "nvim+v2+yazi",
  "windows": [
    { "fork": "1||nvim", "name": "editor", "panes": [{ "cmd": "nvim" }] },
    { "name": "shell", "split": "even-vertical", "panes": [{}, {}] }
  ]
}
```

`split` ∈ `even-horizontal | even-vertical | main-* | tiled`; omitted means bake infers. Empty `cmd` means default shell. Legacy key `layout` is still accepted on parse. Instance presets may additionally carry `cwd`.

### Picker sources & ranking

`Source` = `Snapshot()` (paint) + `Refresh()` (background `tea.Cmd`) + `FlattenFilter(query)` (per-source cap / hide-when-typing). Order, dedup first-wins by name *and* normalized path: create → tmux → preset → zoxide. Create hides itself once a query is typed; zoxide caps at `ZoxideCap` (40) only when the query is empty.

#### Cold start — where the time actually goes (measured, do not re-derive)

Measured with `hyperfine` on ~300 zoxide entries. `gotomux -p` profiles the **standalone** path only; it never consults the daemon.

- **The zoxide cache is keyed by content, not by age.** Deriving rows from raw paths costs ~0.9ms *per path* — `project.Session` → `FindProjectRoot` walks up stat-ing for project markers, ~10k `stat()` calls for a 300-entry list, ~270ms cold — plus ~50ms for the `zoxide query -l` fork itself. A 30s time-based expiry used to discard the already-derived rows in `zox_item`, so **almost every invocation paid ~340ms** (30s is shorter than the gap between two picker opens). Now: `zox_meta.sig` stores `zoxide.Signature(paths)`; the paint serves persisted rows regardless of age, and the background `Refresh` re-derives only when the path list actually changed. The one remaining synchronous rebuild is an empty `zox_item`, i.e. genuinely the first run ever. Do not reintroduce an age-based expiry.
- **Deriving the ranking context must not fork.** `newContext` used to call `CurrentSession` + `CurrentSessionPath` — two tmux forks, ~5.5ms, roughly half the standalone construction — for data already available: `$TMUX`'s third field is the session index and `LiveSession.ID` carries `#{session_id}`. It now resolves from the live list already fetched, with `CurrentContext` (one fork) only as a fallback. The session id enters through `Deps.SessionID` so nothing deep in the package reads the environment.
- **Binary size is not a lever.** `modernc.org/sqlite` is +4.3 MB of the 9.7 MB CLI, but demand paging never reads unused pages: cold `gotomux -v` is 5ms against 3ms for a 1.6 MB binary. Removing sqlite from the CLI would buy ~2ms and cost a large refactor.
- **The daemon must checkpoint the WAL.** It holds the connection open for its whole life, so SQLite never gets the last-connection-close that normally checkpoints; the WAL was observed at 1.3 MB against a 124 KB database, and every client read walks the WAL index. `store.Checkpoint()` (PASSIVE) runs on the 60s zoxide cadence — often, so it stays cheap.

Current numbers: standalone ~13ms warm, ~16ms after an idle gap; startup alone (`-v`) ~5ms; IPC round trip ~1.2ms for a 45 KB payload; building the model from a payload ~150µs.

Ranking (`internal/picker/score.go`) is a **tiered tuple sort**, not a score sum: `tier > recency > cooccur > kind > detail > pathQ > idx`. Frecency comes from the `usage` table (day-decayed opens minus kill penalty, pure integer math). Multi-token queries AND together: tier = worst token's tier, detail = sum. Matching folds diacritics (Vietnamese `đ`→`d`) and splits labels on delimiters plus CamelCase/acronym boundaries.

Inside tmux (`Context.Session` set), items matching the current session name or path are dropped and co-occurrence scores from the `pair` table apply. Outside tmux, everything is visible and cooccur is 0. Same algorithm either way — only the inputs change.

To add a source: implement `Source`, register in `defaultSources`, and give it a `Kind` in `kindRank`. Remote-tmux is the deferred plan (`docs/todo.md`); it would connect by `Item.Src`/`Host`.

### Store

`$XDG_DATA_HOME/gotomux/state.db`, pure-Go `modernc.org/sqlite`, WAL + `synchronous=NORMAL` + 1s busy timeout. Tables: `session`/`window`/`pane` (presets, cascade delete), `usage`, `pair`, `zox_meta`/`zox_item`, `shape`, `sticky`, `placement`, `fork`.

Migration is additive-only and idempotent: `CREATE TABLE IF NOT EXISTS` plus `pragma_table_info` probes for columns added later (`window.cwd`, `shape.updated_at`). Follow that pattern — never drop or rewrite a table.

`store.Storer` is the interface everything depends on; `*store.Store` is the only implementation outside tests. Add methods there, not on the concrete type alone.

### Connect behavior

Inside tmux (`$TMUX` set): `SwitchClient`. Outside: `Attach`. `Load` is a no-op if the session already exists. Enter on Create/Zoxide: live session? attach : same-name preset? load : bake sticky shape into the project root.

`project.FindProjectRoot` walks up for `project.godot`, `.git`, `package.json`, `Cargo.toml`, `go.mod`. `project.SessionName` sanitizes the basename to `[a-z0-9-]`.

## Config

All tunables are env vars parsed by `caarlos0/env` into `config.Config` (`internal/config/config.go`) — add new ones there, not as scattered `os.Getenv` calls, and thread `*config.Config` through rather than reading the environment deep in a package.

| Var | Default | Effect |
| --- | --- | --- |
| `GOTOMUX_DATA_DIR` / `GOTOMUX_CONFIG_DIR` | XDG | override base dirs |
| `GOTOMUX_POLL_INTERVAL` | `10s` | daemon poll |
| `GOTOMUX_ZOXIDE_CAP` | `40` | zoxide items when query empty |
| `GOTOMUX_MAX_SHOW` | `12` | visible rows |
| `GOTOMUX_GIT_CONCURRENCY` | `4` | git enrich workers |
| `GOTOMUX_PROC_CACHE_TTL` | `2s` | pane process detection cache |
| `GOTOMUX_PRUNE_CUTOFF` | `720h` | stale row prune |

Read directly (not in `Config`): `GOTOMUX_ASCII=1` / `GOTOMUX_NERD=1` (icons), `EDITOR`/`VISUAL`.

## External deps

`charm.land/bubbletea/v2` + `bubbles/v2` + `lipgloss` (TUI), `junegunn/fzf/src/algo` (fuzzy core, needs `algo.Init` in `main`), `modernc.org/sqlite`, `shirou/gopsutil/v4` + `/proc` (freeze cmd detection, Linux), `richardwooding/projectdetect`, `epilande/go-devicons`, `caarlos0/env/v11`. Runtime: `tmux` required, `zoxide` optional.

Session listing/attach is done with hand-built tmux commands in `internal/tmux` (both `exec` and the control socket) — there is no tmux client library in the tree.
