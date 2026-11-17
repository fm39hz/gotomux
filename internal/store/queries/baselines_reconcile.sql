-- name: BaselineNameByLogicalID :one
SELECT name FROM baseline WHERE logical_id = ?;

-- name: BaselineLogicalIDByName :one
SELECT logical_id FROM baseline WHERE name = ?;

-- name: UpdateBaselineByLogicalID :exec
UPDATE baseline SET name = ?, body = ?, updated_at = ?, logical_id = ? WHERE logical_id = ?;

-- name: UpdateBaselineByName :exec
UPDATE baseline SET body = ?, updated_at = ?, logical_id = ? WHERE name = ?;

-- name: InsertBaseline :exec
INSERT INTO baseline(name, body, updated_at, logical_id) VALUES(?, ?, ?, ?);

-- name: BaselineByName :one
SELECT body, logical_id FROM baseline WHERE name = ?;

-- name: BaselineByLogicalID :one
SELECT body, logical_id FROM baseline WHERE logical_id = ?;

-- name: InsertReconcileJournal :exec
INSERT INTO reconcile_journal
  (id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ReconcileStatusAndStep :one
SELECT status, completed_step FROM reconcile_journal WHERE id = ?;

-- name: UpdateReconcileJournal :exec
UPDATE reconcile_journal
SET status = ?, completed_step = ?, last_error = ?, updated_at = ?
WHERE id = ?;

-- name: ReconcileJournalByID :one
SELECT id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at
FROM reconcile_journal WHERE id = ?;

-- name: ActiveReconcileBySessionKey :one
SELECT id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at
FROM reconcile_journal
WHERE session_key = ? AND status IN ('planned', 'running', 'needs_attention');

-- name: ActiveReconciles :many
SELECT id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at
FROM reconcile_journal
WHERE status IN ('planned', 'running', 'needs_attention') ORDER BY created_at, id;

-- name: ReconcileIdentityAndStatus :one
SELECT session_key, session_logical_id, status FROM reconcile_journal WHERE id = ?;

-- name: CompleteReconcileJournal :exec
UPDATE reconcile_journal SET status = ?, last_error = '', updated_at = ? WHERE id = ? AND status = ?;

-- name: AbortReconcilesForFreeze :exec
UPDATE reconcile_journal
SET status = 'aborted', last_error = 'superseded by explicit freeze', updated_at = ?
WHERE status IN ('planned', 'running', 'needs_attention')
  AND (session_logical_id = ? OR session_name = ?);

-- name: AbortReconcilesForRevision :exec
UPDATE reconcile_journal
SET status = 'aborted', last_error = 'superseded by explicit recipe edit', updated_at = ?
WHERE status IN ('planned', 'running', 'needs_attention') AND session_logical_id = ?;
