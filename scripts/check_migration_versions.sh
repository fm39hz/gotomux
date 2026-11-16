#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../internal/store/migrations"
expected=1
while IFS= read -r filename; do
  [[ -n "$filename" ]] || continue
  version="${filename%%_*}"
  if [[ ! "$version" =~ ^[0-9]{5}$ ]] || ((10#$version != expected)); then
    printf 'migration versions must be unique, zero-padded, and contiguous from 00001; found %s while expecting %05d\n' "$filename" "$expected" >&2
    exit 1
  fi
  expected=$((expected + 1))
done < <(find . -maxdepth 1 -type f -name '[0-9][0-9][0-9][0-9][0-9]_*.sql' -printf '%f\n' | sort)

if ((expected == 1)); then
  echo 'no Goose SQL migrations found' >&2
  exit 1
fi
