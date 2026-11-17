# tmux client library assessment

**Date:** 2026-11-16  
**Scope:** `internal/tmux` subprocess commands, control mode, and `internal/tmuxlayout`.  
**Decision:** Adopt `github.com/zigai/gotmux v0.5.2` for raw subprocess command execution. Keep gotomux's control-mode lifecycle and layout parser as application-owned code.

## Current seams

- `tmuxCmd` and `tmuxRun` are shared argv-based execution boundaries. Callers depend on trimmed stdout, wrapped errors, stderr diagnostics, and tmux's current socket selection.
- `Ctl.runChain` sends one client process a semicolon-separated command chain.
- `ControlConn` owns a long-lived `tmux -C` process, serializes request/reply exchanges, separates asynchronous events from reply blocks, suppresses pane output, handles `%exit`, and reconnects explicitly. The measured constraints are recorded in [control-mode.md](control-mode.md).
- `ParseLiveOutput` decodes gotomux-specific tabular formats used by standalone and daemon paths.
- `internal/tmuxlayout` normalizes legacy and JSON layout representations, strips volatile data for topology comparisons, and remaps runtime pane IDs and indices.

## Candidate comparison

| Candidate | Pin | Relevant contract | Fit |
| --- | --- | --- | --- |
| [gotmux](https://pkg.go.dev/github.com/zigai/gotmux/tmux@v0.5.2) | `v0.5.2` | MIT; module declares Go 1.26.0 and toolchain 1.27.1. README advertises tmux 3.6+. Exposes raw argv execution, explicit start policy, typed objects, and control mode. Pre-1.0 API. | Selected for the raw command adapter: `RunWith`/`RunSequenceWith` permit explicit no-start behavior, and current Go version meets its requirement. |
| [libtmux-go](https://pkg.go.dev/github.com/libtmux/libtmux-go/tmux@v0.1.2) | `v0.1.2` | MIT; Go 1.26+. Published docs report tmux 3.3a+ for ordinary operations and 3.7+ for owned control connections. The API is unsettled; the project recommends pinning and says it is not recommended for production. | Good snapshot/query/plan surface, but a weaker fit for the long-lived daemon transport under its published control-mode support floor and maturity statement. |

The repository's declared Go version is 1.26.5. Product minimum tmux support is not stated in the repository instructions; control-mode behavior has been verified on tmux 3.7b.

## gotmux source findings and adapter contract

The source checkout at `/tmp/gotomux-library-cache/github.com/zigai/gotmux@v0.5.2` was inspected directly.

### Command execution and startup policy

- `gotmux.New(gotmux.Config{})` validates and snapshots configuration but does not launch a process. A nil `Config.Env` copies `os.Environ()` at construction time.
- `Server.RunWith` takes an explicit `RunOptions.Start`; it does not infer startup permission from the command name. `ExistingOnly` causes `baseArgs` to include `-N`. This is why every executor call must set policy explicitly.
- The library's own `commandStartsServer` recognizes exactly `new-session` and `start-server`. gotomux's adapter mirrors those two command names: direct calls may start only for those commands; sequences may start only when their first non-empty command is one of them. Other calls use `ExistingOnly`.
- `RunSequenceWith` executes a validated sequence in one tmux process and accepts the same explicit start policy, so it is suitable for the current `runChain` boundary.
- `Result` contains distinct `Stdout` and `Stderr` buffers. A wrapped `exec.ExitError` does not get its `Stderr` field populated by gotmux automatically. `executor.go` copies the captured stderr onto that error so `errors.As` and `IsNoServerError` keep working.

### Socket and environment selection

`Config.Env == nil` captures the live process environment. When `$TMUX` is non-empty, gotmux parses its socket path and emits `-S <socket-path>`; it does not silently select the default socket. Otherwise it selects the default named socket with `-L default`. For named sockets it resolves `$TMUX_TMPDIR` (or `/tmp` when unset), normalizes it, and passes that value in the subprocess environment. Creating a fresh `Server` per adapter call preserves environment changes between calls rather than freezing the first one.

### tmux version floor: important distinction

The gotmux README advertises tmux 3.6+. `version.go` defines a 3.6 minimum, and `supportedVersion` rejects older servers when called by version-aware typed operations such as `Probe`.

The adapter uses the raw `RunWith` and `RunSequenceWith` methods. Their source paths do not call `supportedVersion`; they validate the command and pass it to tmux. Therefore, the library's advertised 3.6 floor is not an enforced floor on these raw calls. The actual compatibility floor for this adapter depends on the tmux CLI features it uses, including `-N`; this source audit did not establish a lower supported product floor. Keep the product minimum explicit and verify that floor with isolated tmux-version coverage before relying on older releases.

### Behavioral edge

`tmuxRun` can include both output buffers in an error diagnostic, but gotmux captures stdout and stderr separately. If a command writes to both concurrently, concatenating the buffers cannot reproduce the exact interleaving that `exec.Cmd.CombinedOutput` observed. Current tmux command failures normally report their diagnostic on stderr; preserve that diagnostic and treat byte-for-byte stream interleaving as unproven parity.

## Control mode: library transport and application lifecycle

gotmux v0.5.2 has `Server.OpenControl`, `OpenControlNewSession`, `Connection.Events`, `Wait`, `Close`, and `Refresh`. `ControlConn` now wraps this transport. Gotomux owns lifecycle policy and preserves these constraints:

- The daemon must not start the tmux server. `OpenControlNewSession` creates and attaches a session; it is not a safe startup guard. Any create-or-reuse of `__gotomuxd` must first use an existing-server-only operation, then attach to that session.
- gotmux connections are owned by a context and do not reconnect themselves. The daemon still needs its current retry/backoff policy around connection termination.
- Current daemon queries rely on custom format strings and user-option identity markers. The library's typed snapshots are not established as a wire-compatible replacement for the existing query and `ParseLiveOutput` path.
- Keep the hidden session, no-output setting, membership events plus periodic timestamp polling, `%exit` recovery, and never-start-server behavior from [control-mode.md](control-mode.md).

libtmux-go v0.1.2 also has persistent control APIs, but its docs say control mode needs tmux 3.7+, the attached client changes session attachment state, and closing the connection is terminal rather than reconnecting. Those are material lifecycle constraints for this daemon.

## Layout parsing: keep `internal/tmuxlayout`

gotmux exposes named-layout types, serialized layout text, and layout-change events, but no parser/remapper equivalent to gotomux's topology normalization and pane-ID/index remapping. libtmux-go's snapshots and layout validation likewise do not establish that contract. Keep `internal/tmuxlayout` authoritative for legacy checksum layouts and tmux 3.8 JSON leaves.

## Implemented boundary

`internal/tmux/executor.go` provides the library-backed `tmuxCmd`, `tmuxRun`, and `tmuxRunChain` implementations. `Ctl.runChain` delegates to `tmuxRunChain`. The adapter keeps user-selected socket behavior, permits startup only for explicit `new-session`/`start-server` entry commands, restores `exec.ExitError.Stderr`, and centralizes raw commands from both standalone and daemon queries. Control framing and event decoding belong to gotmux; reconnect/hidden-session policy, domain output parsing and layout mapping belong to gotomux.

Daemon query output uses the shared subprocess sequence executor; the long-lived control connection provides events and liveness. It does not carry these raw list queries.

The focused executor tests use a fake `tmux` executable. They do not invoke a live tmux server or access the store. Test execution and any integration fixes are owned by the main agent.

## Version-pinned source references

- [gotmux README](https://github.com/zigai/gotmux/blob/v0.5.2/README.md) — published Go and tmux requirements.
- [gotmux config.go](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/config.go#L76-L81) — nil environment snapshots `os.Environ()`.
- [gotmux server.go](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/server.go#L239-L278) — `-N`, socket selector, and config arguments; [endpoint resolution](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/server.go#L348-L381) — `$TMUX` and `$TMUX_TMPDIR` handling.
- [gotmux command.go](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/command.go#L588-L594) — startup command names; [`RunWith`](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/command.go#L803-L834) and [`RunSequenceWith`](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/command.go#L836-L880) — explicit policy paths.
- [gotmux version.go](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/version.go#L122-L128) — 3.6 typed-operation version check.
- [gotmux errors.go](https://github.com/zigai/gotmux/blob/v0.5.2/tmux/errors.go#L147-L155) — captured result on `CommandError`.
- [libtmux-go official Go core docs](https://libtmux.org/en/go/latest/core/) and [versioned API docs](https://pkg.go.dev/github.com/libtmux/libtmux-go/tmux@v0.1.2).
