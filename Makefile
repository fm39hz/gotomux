BIN     := gotomux
DAEMON  := gotomuxd
# A dev binary must not print a bare tag: "0.4.5" built three commits past
# v0.4.5 is indistinguishable from the real release, and the commit is the
# first thing you need when a bug gets reported. Same derivation as pkgver()
# in PKGBUILD, so build / install / run / makepkg all agree. --dirty stops an
# uncommitted tree from claiming to be exactly its tag commit; no git or no
# tag yet leaves the compiled-in "dev" fallback in place.
VERSION := $(shell git describe --tags --long --dirty --match 'v*' 2>/dev/null | sed -E 's/^v//; s/-([0-9]+)-g/.r\1.g/; s/-/./g')
LDFLAGS := -s -w $(if $(VERSION),-X main.version=$(VERSION))
REMOTE  := origin
BRANCH  := master

.PHONY: help build build-all run test test-v bench schema install install-all clean fmt vet pkg pkg-install

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## build ./gotomux
	go build -ldflags='$(LDFLAGS)' -o $(BIN) .

build-all: build ## build both CLI and daemon
	go build -ldflags='$(LDFLAGS)' -o $(DAEMON) ./cmd/gotomuxd/

run: ## run picker (ARGS='-h')
	go run -ldflags='$(LDFLAGS)' . $(ARGS)

test: ## unit + integration tests
	go test ./...

test-v: ## tests verbose
	go test ./... -count=1 -v

bench: ## microbenchmarks
	go test ./internal/picker/ -bench=. -benchmem -run=^$$

schema: ## regenerate schema/config.json
	GOTOMUX_UPDATE_SCHEMA=1 go test ./internal/config/ -run TestConfigSchemaGolden

install: ## go install CLI
	go install -ldflags='$(LDFLAGS)' .

install-all: install ## install CLI + daemon + systemd unit
	go install -ldflags='$(LDFLAGS)' ./cmd/gotomuxd/
	mkdir -p ~/.config/systemd/user
	cp dist/gotomuxd.service ~/.config/systemd/user/gotomuxd.service
	systemctl --user daemon-reload
	systemctl --user enable gotomuxd 2>/dev/null || true
	# Restart, not just enable --now: a running unit ignores --now, so the new
	# daemon binary would stay unused until the next reboot. The CLI and the
	# daemon silently diverging across versions is how "freeze worked but the
	# preset is stale" confusion happens (seen 2026-08-25: CLI rebuilt mid-day,
	# old daemon process served freezes until evening).
	systemctl --user restart gotomuxd

clean: ## remove local binaries
	rm -f $(BIN) $(DAEMON)

fmt: ## gofmt
	gofmt -w .

vet: ## go vet + tree guards (no exit/fatal in internal/, algo.Init only in main.go)
	go vet ./...
	./scripts/check_no_exit.sh
	./scripts/check_algo_init.sh

pkg: ## build Arch package (artifacts to dist/)
	mkdir -p dist
	PKGDEST=$(CURDIR)/dist makepkg -f -c --cleanbuild --skipinteg
	@ls -1h dist/gotomux-*.pkg.tar.zst 2>/dev/null || true

pkg-install: ## makepkg -si
	PKGDEST=$(CURDIR)/dist makepkg -si --noconfirm -c --cleanbuild --skipinteg
