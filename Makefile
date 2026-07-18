BIN     := gotomux
LDFLAGS := -s -w

.PHONY: help build run test test-v bench install clean fmt vet pkg pkg-install

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## build ./gotomux
	go build -ldflags='$(LDFLAGS)' -o $(BIN) .

run: ## run picker (args: make run ARGS='-h')
	go run . $(ARGS)

test: ## unit + integration tests
	go test ./...

test-v: ## tests verbose
	go test ./... -count=1 -v

bench: ## microbenchmarks (needs tmux for some)
	go test ./internal/picker/ -bench=. -benchmem -run=^$$

install: ## go install to $$(go env GOPATH)/bin
	go install -ldflags='$(LDFLAGS)' .

clean: ## remove local binary
	rm -f $(BIN)

fmt: ## gofmt
	gofmt -w .

vet: ## go vet
	go vet ./...

pkg: ## build Arch package in dist/ (makepkg)
	cd dist && makepkg -f --cleanbuild --skipinteg
	@ls -1h dist/gotomux-*.pkg.tar.zst 2>/dev/null || true

pkg-install: ## makepkg -si local package (needs sudo/pacman)
	cd dist && makepkg -si --noconfirm --cleanbuild --skipinteg

