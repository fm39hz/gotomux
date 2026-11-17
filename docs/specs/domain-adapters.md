# Domain and infrastructure boundaries

Session ownership, saved replay, frozen desired state and runtime incarnation
are domain concepts in internal/model. Repository contracts are defined there;
internal/store implements them with sqlc and database/sql transactions. Store
aliases preserve source compatibility without defining a second contract.

All raw tmux command execution is owned by internal/tmux/executor.go, including
daemon list queries. That boundary constructs gotmux servers, selects startup
policy and preserves stderr/error semantics. Typed gotmux lifecycle operations
(control connection, event stream and interactive attach) use the same server
factory. ControlConn owns reconnect, timeout and hidden-session policy; gotmux
owns transport framing and event decoding. List queries currently execute as
subprocess sequences, not through the event connection.

Application services retain identity validation, freeze, reconcile planning,
recovery and postcondition verification. These rules cannot be delegated to a
generic tmux client. The layout mapper is pure domain-supporting code because
stable pane-slot replay is not supplied by the pinned client library.

SQLite application queries live in store/queries and are compiled by sqlc.
Goose owns forward migration execution. migration_adoption.go is the explicit
compatibility boundary for schema-verified pre-Goose/adoption-era databases;
its history repair remains intentionally available for affected installations.
It does not implement new application schema migrations. migrate.go owns only
provider configuration, execution and final version checks. PRAGMA connection
settings and WAL checkpointing remain adapter responsibilities.

The supported architecture permits raw argv inside the tmux adapter and raw
SQL inside migration compatibility/PRAGMA code. Application code must consume
domain contracts instead of importing gotmux, dbgen or SQLite connections.
