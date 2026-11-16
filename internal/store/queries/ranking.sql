-- name: RecordOpen :exec
INSERT INTO usage(name, opens, kills, last_open, last_kill) VALUES(?, 1, 0, ?, 0)
ON CONFLICT(name) DO UPDATE SET opens = opens + 1, last_open = excluded.last_open;

-- name: RecordKill :exec
INSERT INTO usage(name, opens, kills, last_open, last_kill) VALUES(?, 0, 1, 0, ?)
ON CONFLICT(name) DO UPDATE SET kills = kills + 1, last_kill = excluded.last_kill;

-- name: AllUsage :many
SELECT name, opens, kills, last_open, last_kill FROM usage;

-- name: RecordPair :exec
INSERT INTO pair(a, b, n, last) VALUES(?, ?, 1, ?)
ON CONFLICT(a, b) DO UPDATE SET n = n + 1, last = excluded.last;

-- name: PairRowsForSession :many
SELECT a, b, n, last FROM pair WHERE a = ? OR b = ?;

-- name: RecordTransition :exec
INSERT INTO transition(prev, next, n, last) VALUES(?, ?, 1, ?)
ON CONFLICT(prev, next) DO UPDATE SET n = n + 1, last = excluded.last;

-- name: TransitionRowsFrom :many
SELECT next, n, last FROM transition WHERE prev = ?;

-- name: UsageByName :one
SELECT opens, kills, last_open, last_kill FROM usage WHERE name = ?;

-- name: MergeUsage :exec
INSERT INTO usage(name, opens, kills, last_open, last_kill) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
  opens = opens + excluded.opens,
  kills = kills + excluded.kills,
  last_open = MAX(last_open, excluded.last_open),
  last_kill = MAX(last_kill, excluded.last_kill);

-- name: DeleteUsageByName :exec
DELETE FROM usage WHERE name = ?;

-- name: PairRowsForRename :many
SELECT a, b, n, last FROM pair WHERE a = ? OR b = ?;

-- name: DeletePair :exec
DELETE FROM pair WHERE a = ? AND b = ?;

-- name: MergePair :exec
INSERT INTO pair(a, b, n, last) VALUES(?, ?, ?, ?)
ON CONFLICT(a, b) DO UPDATE SET n = n + excluded.n, last = MAX(last, excluded.last);

-- name: TransitionRowsForRename :many
SELECT prev, next, n, last FROM transition WHERE prev = ? OR next = ?;

-- name: DeleteTransition :exec
DELETE FROM transition WHERE prev = ? AND next = ?;

-- name: MergeTransition :exec
INSERT INTO transition(prev, next, n, last) VALUES(?, ?, ?, ?)
ON CONFLICT(prev, next) DO UPDATE SET n = n + excluded.n, last = MAX(last, excluded.last);
