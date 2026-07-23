#!/usr/bin/env bash
set -euo pipefail

usage() {
	echo "Usage: $0 [--do] [major|minor|patch]"
	echo "  Default: dry-run (print next version)"
	echo "  --do: create tag, push branch + tag"
	exit 1
}

do_push=false
level="${1:-patch}"
if [ "$level" = "--do" ]; then
	do_push=true
	level="${2:-patch}"
fi

case "$level" in
	major|minor|patch) ;;
	*) usage ;;
esac

latest=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
raw=${latest#v}

IFS=. read -r major minor patch <<< "$raw"
major=${major:-0}
minor=${minor:-0}
patch=${patch:-0}

case "$level" in
	major) major=$((major+1)); minor=0; patch=0 ;;
	minor) minor=$((minor+1)); patch=0 ;;
	patch) patch=$((patch+1)) ;;
esac

tag="v${major}.${minor}.${patch}"
echo "${latest} -> ${tag}"

if $do_push; then
	if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
		echo "fatal: uncommitted changes" >&2
		exit 1
	fi
	git tag "$tag" && git push origin master && git push origin "$tag"
fi
