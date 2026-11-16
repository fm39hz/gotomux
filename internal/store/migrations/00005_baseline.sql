-- +goose Up
CREATE TABLE baseline (
  name TEXT PRIMARY KEY,
  body TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
