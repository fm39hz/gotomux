-- +goose Up
ALTER TABLE pane ADD COLUMN start_cmd TEXT NOT NULL DEFAULT '';
