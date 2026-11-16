-- +goose Up
ALTER TABLE pane ADD COLUMN cmd_path TEXT NOT NULL DEFAULT '';
