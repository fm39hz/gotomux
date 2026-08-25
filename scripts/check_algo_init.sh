#!/usr/bin/env bash
# P2.5 guard: algo.Init must run exactly once, from root main.go.
#
# junegunn/fzf/src/algo Init mutates package-level globals without any
# synchronization (_ref/fzf/src/algo/algo.go:176-215). gotomux links that code
# into a binary that also runs a multi-goroutine daemon, so a second Init call
# racing live match goroutines is an intermittent data race — the hardest kind
# to trace back. One call site, before any matching starts.
#
# Wired into `make vet`.
set -u

cd "$(dirname "$0")/.." || exit 1

violations=$(grep -RIn \
	--include='*.go' \
	--exclude-dir=_ref \
	--exclude-dir=vendor \
	--exclude-dir=.git \
	-e 'algo\.Init(' \
	. | grep -v '^./main\.go:') || true

if [ -n "$violations" ]; then
	echo "check_algo_init: algo.Init( called outside main.go:" >&2
	printf '%s\n' "$violations" >&2
	echo "rule: algo.Init mutates unsynchronized globals; call it exactly once, from the init() in main.go. If a runtime scheme switch is ever needed, extract src/algo into an internal package instead of re-initializing." >&2
	exit 1
fi
