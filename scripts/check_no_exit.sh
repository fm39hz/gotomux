#!/usr/bin/env bash
# P3.3 guard: library code must not terminate the process.
#
# os.Exit/log.Fatal deep in a package is untestable and kills whatever binary
# linked it (the sesh convert.StringToTime failure mode). Termination belongs
# to entrypoints only: root main.go and cmd/*/main.go are whitelisted by being
# outside the scanned tree. panic( stays legal — it means "programming bug",
# not "policy exit".
#
# Wired into `make vet`.
set -u

cd "$(dirname "$0")/.." || exit 1

violations=$(grep -RIn \
	--include='*.go' \
	--exclude='*_test.go' \
	--exclude-dir=_ref \
	--exclude-dir=vendor \
	-e 'os\.Exit(' -e 'log\.Fatal' \
	internal/ || true)

if [ -n "$violations" ]; then
	echo "check_no_exit: os.Exit/log.Fatal found outside entrypoints:" >&2
	printf '%s\n' "$violations" >&2
	echo "rule: library packages (internal/**) must return errors, never terminate the process; exit/fatal belongs in main.go or cmd/*/main.go" >&2
	exit 1
fi
