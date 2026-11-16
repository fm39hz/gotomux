-- name: ZoxMeta :one
SELECT updated, sig FROM zox_meta WHERE id = 1;

-- name: ZoxItems :many
SELECT name, path, title, desc, recency FROM zox_item ORDER BY ord;

-- name: DeleteZoxItems :exec
DELETE FROM zox_item;

-- name: UpsertZoxMeta :exec
INSERT INTO zox_meta(id, updated, sig) VALUES(1, ?, ?)
ON CONFLICT(id) DO UPDATE SET updated = excluded.updated, sig = excluded.sig;

-- name: InsertZoxItem :exec
INSERT INTO zox_item(ord, name, path, title, desc, recency) VALUES(?, ?, ?, ?, ?, ?);

-- name: RootNegChecked :one
SELECT checked FROM root_neg WHERE path = ?;

-- name: UpsertRootNeg :exec
INSERT INTO root_neg(path, checked) VALUES(?, ?)
ON CONFLICT(path) DO UPDATE SET checked = excluded.checked;

-- name: DeleteRootNeg :exec
DELETE FROM root_neg WHERE path = ?;

-- name: PrunePlacement :exec
DELETE FROM placement AS p
WHERE p.n < 2 AND p.last < ?
  AND EXISTS (
    SELECT 1 FROM placement p2
    WHERE p2.shape_id = p.shape_id AND p2.n > p.n
  );

-- name: PruneFork :exec
DELETE FROM fork WHERE n < 2 AND last < ?;

-- name: RecordPlacement :exec
INSERT INTO placement(shape_id, pattern, n, last) VALUES(?, ?, 1, ?)
ON CONFLICT(shape_id, pattern) DO UPDATE SET n = n + 1, last = excluded.last;

-- name: BestPlacement :one
SELECT pattern FROM placement WHERE shape_id = ? ORDER BY n DESC, last DESC LIMIT 1;

-- name: RecordFork :exec
INSERT INTO fork(key, body, n, last) VALUES(?, ?, 1, ?)
ON CONFLICT(key) DO UPDATE SET
  n = n + 1,
  last = excluded.last,
  body = CASE WHEN excluded.body != '' THEN excluded.body ELSE body END;

-- name: ForkHits :one
SELECT n FROM fork WHERE key = ?;
