-- +goose Up
ALTER TABLE baseline ADD COLUMN logical_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_baseline_logical_id ON baseline(logical_id) WHERE logical_id <> '';
CREATE TABLE reconcile_journal (
  id TEXT PRIMARY KEY,
  session_key TEXT NOT NULL,
  session_name TEXT NOT NULL,
  session_logical_id TEXT NOT NULL DEFAULT '',
  policy TEXT NOT NULL,
  status TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  completed_step INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_reconcile_active_session
  ON reconcile_journal(session_key)
  WHERE status IN ('planned', 'running', 'needs_attention');
