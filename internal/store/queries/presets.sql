-- name: ListPresetMeta :many
SELECT name, cwd, last_used FROM session ORDER BY last_used DESC, name;

-- name: PresetByName :one
SELECT name, cwd, logical_id FROM session WHERE name = ?;

-- name: PresetWindows :many
SELECT id, idx, name, COALESCE(cwd, '') AS cwd, COALESCE(layout, '') AS layout, slot_id
FROM window WHERE session = ? ORDER BY idx;

-- name: PresetPanes :many
SELECT id, idx, COALESCE(cwd, '') AS cwd, COALESCE(cmd, '') AS cmd, COALESCE(cmd_path, '') AS cmd_path, slot_id
FROM pane WHERE window_id = ? ORDER BY idx;

-- name: DeleteSession :exec
DELETE FROM session WHERE name = ?;

-- name: DeleteSessionsByCwdExcept :exec
DELETE FROM session WHERE cwd = ? AND name != ?;

-- name: SessionNames :many
SELECT name FROM session;

-- name: CountSessionRows :one
SELECT COUNT(*) FROM session;

-- name: TouchSession :exec
UPDATE session SET last_used = ? WHERE name = ?;

-- name: InsertSession :exec
INSERT INTO session(name, cwd, created_at, last_used, logical_id) VALUES(?, ?, ?, ?, ?);

-- name: InsertWindow :execresult
INSERT INTO window(session, idx, name, cwd, layout, slot_id) VALUES(?, ?, ?, ?, ?, ?);

-- name: InsertPane :exec
INSERT INTO pane(window_id, idx, cwd, cmd, cmd_path, slot_id) VALUES(?, ?, ?, ?, ?, ?);

-- name: UpdatePresetLogicalID :exec
UPDATE session SET logical_id = ? WHERE name = ? AND (logical_id = '' OR logical_id = ?);

-- name: UpdatePresetWindowSlot :exec
UPDATE window SET slot_id = ? WHERE id = ? AND (slot_id = '' OR slot_id = ?);

-- name: UpdatePresetPaneSlot :exec
UPDATE pane SET slot_id = ? WHERE id = ? AND (slot_id = '' OR slot_id = ?);
