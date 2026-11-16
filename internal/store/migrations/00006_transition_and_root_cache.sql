-- +goose Up
CREATE TABLE transition (
  prev TEXT NOT NULL,
  next TEXT NOT NULL,
  n INTEGER NOT NULL DEFAULT 0,
  last INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (prev, next)
);
CREATE TABLE root_neg (
  path TEXT PRIMARY KEY,
  checked INTEGER NOT NULL
);
