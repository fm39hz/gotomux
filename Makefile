BIN     := gotomux
DAEMON  := gotomuxd
LDFLAGS := -s -w
REMOTE  := origin
BRANCH  := master

.PHONY: help build build-all run test test-v bench install install-all clean fmt vet pkg pkg-install publish

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## build ./gotomux
	go build -ldflags='$(LDFLAGS)' -o $(BIN) .

build-all: build ## build both CLI and daemon
	go build -ldflags='$(LDFLAGS)' -o $(DAEMON) ./cmd/gotomuxd/

run: ## run picker (ARGS='-h')
	go run . $(ARGS)

test: ## unit + integration tests
	go test ./...

test-v: ## tests verbose
	go test ./... -count=1 -v

bench: ## microbenchmarks
	go test ./internal/picker/ -bench=. -benchmem -run=^$$

install: ## go install CLI
	go install -ldflags='$(LDFLAGS)' .

install-all: install ## install CLI + daemon + systemd unit
	go install -ldflags='$(LDFLAGS)' ./cmd/gotomuxd/
	mkdir -p ~/.config/systemd/user
	cp dist/gotomuxd.service ~/.config/systemd/user/gotomuxd.service
	systemctl --user daemon-reload
	systemctl --user enable --now gotomuxd 2>/dev/null || systemctl --user start gotomuxd

clean: ## remove local binaries
	rm -f $(BIN) $(DAEMON)

fmt: ## gofmt
	gofmt -w .

vet: ## go vet
	go vet ./...

publish: ## tag & push: make publish [major|minor|patch]
	@scripts/bump-version.sh --do $(filter-out publish,$(MAKECMDGOALS))

# Make consumes these as targets so declare them phony to avoid confusion.
.PHONY: major minor patch
major minor patch: ;

pkg: ## build Arch package (artifacts to dist/)
	mkdir -p dist
	PKGDEST=$(CURDIR)/dist makepkg -f -c --cleanbuild --skipinteg
	@ls -1h dist/gotomux-*.pkg.tar.zst 2>/dev/null || true

pkg-install: ## makepkg -si
	PKGDEST=$(CURDIR)/dist makepkg -si --noconfirm -c --cleanbuild --skipinteg
