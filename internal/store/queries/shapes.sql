-- name: ShapeIDByKey :one
SELECT id FROM shape WHERE key = ?;

-- name: UpdateShapeBodyByID :exec
UPDATE shape SET body = ?, updated_at = ? WHERE id = ?;

-- name: InsertShape :exec
INSERT INTO shape(id, key, body, created_at, updated_at) VALUES(?, ?, ?, ?, ?);

-- name: ShapeByKey :one
SELECT id, body FROM shape WHERE key = ?;

-- name: ShapeMetaByID :one
SELECT body, COALESCE(updated_at, created_at, 0) AS updated_at,
       COALESCE(mirror_path, '') AS mirror_path,
       COALESCE(mirror_sig, '') AS mirror_sig
FROM shape WHERE id = ?;

-- name: UpdateShapeMirror :exec
UPDATE shape SET mirror_path = ?, mirror_sig = ? WHERE id = ?;

-- name: ShapeBodyByID :one
SELECT body FROM shape WHERE id = ?;

-- name: UpdateShapeBodyAndTime :exec
UPDATE shape SET body = ?, updated_at = ? WHERE id = ?;

-- name: RebindStickyShape :exec
UPDATE sticky SET shape_id = ? WHERE shape_id = ?;

-- name: DeletePlacementForShape :exec
DELETE FROM placement WHERE shape_id = ?;

-- name: DeleteShape :exec
DELETE FROM shape WHERE id = ?;

-- name: UpsertShapeByID :exec
INSERT INTO shape(id, key, body, created_at, updated_at) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  key = excluded.key,
  body = excluded.body,
  updated_at = excluded.updated_at;

-- name: ListShapeIDs :many
SELECT id FROM shape ORDER BY id;

-- name: StickyShapeID :one
SELECT shape_id FROM sticky WHERE id = 1;

-- name: SetStickyShape :exec
INSERT INTO sticky(id, shape_id) VALUES(1, ?)
ON CONFLICT(id) DO UPDATE SET shape_id = excluded.shape_id;
