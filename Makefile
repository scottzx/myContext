# mycontext - personal operations context system, deterministic Go core.
#
# The binary is CGo-free so every target cross-compiles from any host; iSH
# (linux/386) is the first-class platform.

BINARY      := mycontext
PKG         := ./cmd/mycontext
VERSION     ?= 0.1.0-dev
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X github.com/scottzx/mycontext/internal/cli.Version=$(VERSION) \
	-X github.com/scottzx/mycontext/internal/cli.Commit=$(COMMIT) \
	-X github.com/scottzx/mycontext/internal/cli.BuildTime=$(BUILD_TIME)

BUILD_DIR   := build
# First stage targets (technical design §18.3). iSH and macOS ARM are required.
PLATFORMS   := linux/386 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: help
help:
	@echo "make build      Build the frontend, then $(BINARY) for the host"
	@echo "make build-noweb  Build $(BINARY) only, against the existing web/dist"
	@echo "make web        Build the frontend into web/dist"
	@echo "make test       Run the full test suite"
	@echo "make check      Vet, build and test"
	@echo "make release    Cross-compile every first-stage platform into $(BUILD_DIR)/"
	@echo "make ish        Build the iSH (linux/386) binary only"
	@echo "make checksums  Write SHA-256 sums for the release artifacts"
	@echo "make catalog    Regenerate schemas/catalog.json from the command tree"
	@echo "make npm        Assemble the npm publish tree into npm/dist/"
	@echo "make clean      Remove build output"

.PHONY: web
web:
	cd web && npm install --no-audit --no-fund && npm run build

.PHONY: build
build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(PKG)

# Rebuilds the Go binary against whatever is already in web/dist, without
# reinstalling npm deps or rebuilding the frontend. Useful when iterating on
# Go code only.
.PHONY: build-noweb
build-noweb:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(PKG)

.PHONY: test
test:
	go test ./...

.PHONY: check
check: web
	go vet ./...
	CGO_ENABLED=0 go build ./...
	go test ./...

.PHONY: ish
ish:
	CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(BINARY)-linux-386 $(PKG)

.PHONY: release
release: web
	@mkdir -p $(BUILD_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; arch=$${platform##*/}; \
		out=$(BUILD_DIR)/$(BINARY)-$$os-$$arch; \
		printf '%-20s' "$$platform"; \
		if CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags="$(LDFLAGS)" -o $$out $(PKG); then \
			echo "ok  $$(du -h $$out | cut -f1)"; \
		else \
			echo "FAILED"; exit 1; \
		fi; \
	done

# The catalog is what an agent reads to discover operations; it is generated
# from the live cobra tree so it cannot drift from the real commands.
.PHONY: catalog
catalog: build
	@mkdir -p schemas
	./$(BUILD_DIR)/$(BINARY) catalog --format json \
		| python3 -c 'import sys,json; json.dump(json.load(sys.stdin)["data"], open("schemas/catalog.json","w"), ensure_ascii=False, indent=2)'
	@echo "schemas/catalog.json updated"

# Assembles platform packages from `make release` output. Downloads nothing.
.PHONY: npm
npm: release catalog
	node npm/build-packages.js

.PHONY: checksums
checksums:
	cd $(BUILD_DIR) && shasum -a 256 $(BINARY)-* > SHA256SUMS

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR) npm/dist web/dist web/node_modules
