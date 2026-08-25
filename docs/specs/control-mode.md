# tmux control mode — measured constraints

> Migrated from `AGENTS.md` (2026-08-25) per `docs/_improvements.md` §P3.2.
> Verified on tmux 3.7b in throwaway servers (`tmux -L …`). These are not inferable
> from the code and each one killed a plausible design during development.

- **The daemon must never start the tmux server.** tmux registers a systemd *transient scope per pane*, parented under whatever unit started the server. A server started from `gotomuxd.service` therefore makes every pane of every session a child of that unit, and `systemctl --user stop gotomuxd` tears them all down — the journal logs `Stopping tmux child pane <pid>` for each and leaves the server alive with **zero sessions**. This destroyed real sessions during development. `KillMode=process` (now in `dist/gotomuxd.service`) saves the server *process* but not the pane scopes, so it is necessary and **not sufficient**. `daemon.New` therefore never runs `start-server`: it attaches only when `tmux.ServerRunning()` is already true, stays `Ready=false` otherwise (clients fall back to standalone), and `ensureControl` retries each poll. Correct topology, verified: `tmux-spawn-*.scope` units are *siblings* of `gotomuxd.service`, and the service cgroup holds only `gotomuxd` plus its own `tmux -C` client.

- **`exit-empty off` is no longer set.** It mutated the user's server globally and is redundant while the daemon owns a hidden session — the server never reaches zero sessions.

- **A control client must own a session, and attaching to a user session corrupts the data we serve.** `-C attach-session -t X` sets `session_attached=1` and bumps `session_last_attached` + `session_activity` on X. `-r` (read-only) does **not** help — it blocks input, not the attach. Since `LiveSession.Recency` is `max(LastAttached, Activity, Created)`, a daemon that attaches is falsifying its own ranking input.

- **The only non-perturbing invocation is a dedicated hidden session**: `tmux -C new-session -A -s __gotomuxd -- cat`. User sessions stay byte-identical (`attached=0`, `last_attached` still empty on never-attached sessions). `-- cat` avoids spawning a shell — with a real shell the stream floods with `%output` of the prompt. `-A` makes daemon restart reuse the session. The hidden session **is** visible to raw `list-sessions`, so both list producers drop it centrally through `tmux.DropHidden` (`Ctl.ListLive` for the exec path, the daemon's control-socket parse); anything parsing raw output itself must filter it by hand.

- **`refresh-client -f no-output` suppresses `%output` entirely** (client gains the `no-output` flag; measured `%output` count drops to 0). Send it as the first command after connecting.

- **`buildCommand`-style quoting has two independent fatal bugs.** Quote set `" '\";"` omits TAB, so `ListSessFmt` (tab-separated) is split into argv by tmux and only `S` survives as the format. And `";"` *does* match the set, so the separator gets quoted to `';'` → `parse error: command list-sessions: too many arguments` → `%error` → **`%exit`**, killing the client. Correct approach: one command per line, single-quote every argument that is not a bare command/flag token, never quote the separator, and read one `%begin`/`%end` block per command. An unquoted `;` on one line does work but still yields **two** blocks.

- **Membership events**: creating a session emits `%sessions-changed` (plus `%unlinked-window-add`) — there is no `%session-created`. Killing emits `%sessions-changed` + `%unlinked-window-close`. Renaming emits `%session-renamed`. So `%sessions-changed` is the event to key membership resync on.

- **Events do not cover timestamps.** `session_activity` advances on any pane output with no event emitted, and it feeds `Recency`. Event-driven resync therefore does **not** replace the periodic poll — events handle membership, the poll handles timestamps. Keep both.

- **`%exit` means the client is gone** (e.g. after `%error` on a malformed command) — the transport must treat it as a reconnect trigger, not just a log line.

- **`session_last_attached` is empty for never-attached sessions**, so `ParseLiveOutput`'s numeric fields must tolerate `""` (`parseUnix` / discarded `Atoi` error already do).
