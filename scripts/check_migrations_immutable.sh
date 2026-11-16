#!/usr/bin/env bash
set -euo pipefail

base="${GOTOMUX_MIGRATION_BASE:-}"
if [[ -z "$base" ]]; then
  base="$(git rev-parse --verify HEAD^ 2>/dev/null || true)"
fi
if [[ -z "$base" ]] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
  exit 0
fi

while IFS= read -r path; do
  [[ "$path" =~ ^internal/store/migrations/[0-9]{5}_.+\.sql$ ]] || continue
  if ! git cat-file -e "$base:$path" 2>/dev/null; then
    continue
  fi
  if ! git cat-file -e "HEAD:$path" 2>/dev/null || ! git diff --quiet "$base" HEAD -- "$path" || ! git diff --quiet HEAD -- "$path"; then
    printf 'released migration is immutable: %s\n' "$path" >&2
    exit 1
  fi
done < <(git ls-tree -r --name-only "$base" -- internal/store/migrations)
