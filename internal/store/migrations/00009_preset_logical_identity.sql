-- +goose Up
ALTER TABLE session ADD COLUMN logical_id TEXT NOT NULL DEFAULT '';
ALTER TABLE window ADD COLUMN slot_id TEXT NOT NULL DEFAULT '';
ALTER TABLE pane ADD COLUMN slot_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_preset_session_logical_id
  ON session(logical_id) WHERE logical_id <> '';
CREATE UNIQUE INDEX idx_preset_window_slot_id
  ON window(slot_id) WHERE slot_id <> '';
CREATE UNIQUE INDEX idx_preset_pane_slot_id
  ON pane(slot_id) WHERE slot_id <> '';
