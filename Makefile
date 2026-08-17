# Scion Makefile
# Run 'make help' to see available targets.

BINARY        := scion
BUILD_DIR     := ./build
CONTAINER_DIR := ./.build/container
PREFIX        ?= /usr/local
DESTDIR       ?=
INSTALL_DIR   := $(PREFIX)/bin
MAIN_PKG      := ./cmd/scion
LDFLAGS            := $(shell ./hack/version.sh)
SCIONTOOL_LDFLAGS  := $(shell ./hack/version.sh github.com/GoogleCloudPlatform/scion/cmd/sciontool/commands)
CONTAINER_OS  := linux
CONTAINER_ARCH := $(shell if [ "$$(uname -m)" = "x86_64" ]; then echo amd64; else echo arm64; fi)
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null || echo $(shell go env GOPATH)/bin/golangci-lint)

.DEFAULT_GOAL := help

.PHONY: all build build-a2a-bridge install test test-fast vet lint compat-literals dockerfile-stages golangci-lint web web-typecheck web-test fmt fmt-check ci ci-full clean help container-sciontool container-scion container-binaries proto proto-check

## all: Build the web frontend and compile the Go binary (run 'make install' separately to install)
all: web build

## build: Compile the scion binary into ./build/
build:
	@echo "Building $(BINARY)..."
	@mkdir -p $(BUILD_DIR)
	@go build -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(MAIN_PKG)
	@echo "Binary: $(BUILD_DIR)/$(BINARY)"

## build-a2a-bridge: Build the A2A bridge binary into ./bin/
build-a2a-bridge:
	@echo "Building scion-a2a-bridge..."
	@mkdir -p bin
	@go build -o bin/scion-a2a-bridge ./extras/scion-a2a-bridge/cmd/scion-a2a-bridge/
	@echo "Binary: bin/scion-a2a-bridge"

## install: Install a pre-built binary (default: /usr/local/bin, override with PREFIX=~/.local). Run 'make build' first.
install:
	@if [ ! -f $(BUILD_DIR)/$(BINARY) ]; then \
		echo "Error: $(BUILD_DIR)/$(BINARY) not found. Run 'make build' (or 'make all') first."; \
		exit 1; \
	fi
	@echo "Installing $(BINARY) to $(DESTDIR)$(INSTALL_DIR)..."
	@mkdir -p $(DESTDIR)$(INSTALL_DIR)
	@install $(BUILD_DIR)/$(BINARY) $(DESTDIR)$(INSTALL_DIR)/$(BINARY)
	@echo ""
	@echo "✔ Installed $(BINARY) to $(DESTDIR)$(INSTALL_DIR)/$(BINARY)"
	@echo ""
	@echo "  Run 'scion version' to verify."
	@echo ""
	@case ":$$PATH:" in \
		*":$(INSTALL_DIR):"* | *":$(INSTALL_DIR)/:"*) ;; \
		*) echo "  ⚠ WARNING: $(INSTALL_DIR) is not in your PATH."; \
		   echo "  Add it with:"; \
		   echo ""; \
		   echo "    export PATH=\"$(INSTALL_DIR):\$$PATH\""; \
		   echo "" ;; \
	esac

## test: Run all tests
test:
	@echo "Running tests..."
	@go test ./...

## test-fast: Run tests without SQLite (lower memory usage)
test-fast:
	@echo "Running tests (no SQLite)..."
	@go test -tags no_sqlite ./...

## vet: Run go vet
vet:
	@go vet ./...

## lint: Run go vet (no SQLite, memory-safe)
lint:
	@go vet -tags no_sqlite ./...

## compat-literals: Check legacy grove literals stay in compatibility surfaces
compat-literals:
	@./hack/check-project-compat-literals.sh

## dockerfile-stages: Check the root Dockerfile's default build target is still the runtime image
# The self-test belongs to the target, not to the CI step, so that `make ci`
# locally and the CI job run the same two commands. It was briefly in both and
# ran twice; one place is enough, and the guard's own test should travel with
# the guard rather than with one caller.
#
# The self-test's marker line is printed rather than discarded, and both
# directions are pinned. A dropped `--self-test` argument used to be silent: the
# script with no argument runs the ordinary check a second time and exits 0, so
# with stdout discarded the log was byte-indistinguishable from a correct run.
# Command substitution is used instead of a pipe because /bin/sh here is dash,
# which has no `pipefail` -- in a pipeline the script's exit status is discarded
# and a self-test that fails outright would still pass the target.
#
# The second recipe line asserts the marker is PRESENT after `--self-test`. That
# assertion is only meaningful while the marker is ABSENT from the ordinary run,
# which nothing else enforces, so the first line asserts the negative twin: a
# presence-only guard can be made vacuous by an edit to a path it never looks at.
dockerfile-stages:
	@bare=$$(./hack/check-dockerfile-stages.sh) || { printf '%s\n' "$$bare" >&2; exit 1; }; \
	 printf '%s\n' "$$bare"; \
	 if printf '%s\n' "$$bare" | grep -q 'self-test passed:'; then \
	   echo "dockerfile-stages: NEGATIVE TWIN FAILED -- the ordinary run printed the self-test marker, so the check below can no longer tell a real self-test from a dropped flag." >&2; \
	   exit 1; \
	 fi
	@selftest=$$(./hack/check-dockerfile-stages.sh --self-test) || { \
	   printf '%s\n' "$$selftest" >&2; \
	   echo "dockerfile-stages: self-test FAILED (exit status)" >&2; exit 1; }; \
	 if ! printf '%s\n' "$$selftest" | grep -q 'self-test passed: [0-9][0-9]* cases'; then \
	   printf '%s\n' "$$selftest" >&2; \
	   echo "dockerfile-stages: self-test MARKER ABSENT -- the run exited 0 without reporting cases, which is what a dropped --self-test argument looks like." >&2; \
	   exit 1; \
	 fi; \
	 printf '%s\n' "$$selftest" | grep 'self-test passed:'

## golangci-lint: Run golangci-lint on new issues only (install via: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)
golangci-lint:
	@if [ ! -x "$(GOLANGCI_LINT)" ]; then \
		echo "ERROR: golangci-lint not found. Install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; \
	fi
	@echo "Running golangci-lint (new issues vs main)..."
	@GOGC=50 $(GOLANGCI_LINT) run --new-from-rev=main ./...
	@echo "golangci-lint passed."

## web: Build the web frontend
web:
	@echo "Building web frontend..."
	@rm -rf web/dist
	@cd web && npm install && npm run build
	@mkdir -p web/dist/client && touch web/dist/client/.gitkeep
	@echo "Web frontend built."

## container-sciontool: Cross-compile sciontool for Linux containers
container-sciontool:
	@echo "Building sciontool for $(CONTAINER_OS)/$(CONTAINER_ARCH)..."
	@mkdir -p $(CONTAINER_DIR)
	@GOOS=$(CONTAINER_OS) GOARCH=$(CONTAINER_ARCH) CGO_ENABLED=0 \
		go build -buildvcs=false -ldflags "$(SCIONTOOL_LDFLAGS)" \
		-o $(CONTAINER_DIR)/sciontool ./cmd/sciontool
	@echo "Built: $(CONTAINER_DIR)/sciontool"

## container-scion: Cross-compile scion CLI for Linux containers
container-scion:
	@echo "Building scion for $(CONTAINER_OS)/$(CONTAINER_ARCH)..."
	@mkdir -p $(CONTAINER_DIR)
	@GOOS=$(CONTAINER_OS) GOARCH=$(CONTAINER_ARCH) CGO_ENABLED=0 \
		go build -buildvcs=false -tags no_embed_web -ldflags "$(LDFLAGS)" \
		-o $(CONTAINER_DIR)/scion ./cmd/scion
	@echo "Built: $(CONTAINER_DIR)/scion"

## container-binaries: Build both scion and sciontool for Linux containers
container-binaries: container-sciontool container-scion
	@echo ""
	@echo "Dev binaries ready in $(CONTAINER_DIR)/"
	@echo "Usage: export SCION_DEV_BINARIES=$(CONTAINER_DIR)"

## web-typecheck: Run TypeScript type checking on the web frontend
web-typecheck:
	@echo "Type-checking web frontend..."
	@cd web && npm run typecheck
	@echo "Type check passed."

## web-test: Run the web frontend unit tests (vitest)
web-test:
	@echo "Running web frontend tests..."
	@cd web && npm test
	@echo "Web tests passed."

## fmt: Auto-format Go source files
fmt:
	@echo "Formatting Go source files..."
	@gofmt -w .
	@echo "Go formatting done."

## fmt-check: Check Go formatting without modifying files (mirrors GitHub Actions)
fmt-check:
	@echo "Checking Go formatting..."
	@UNFORMATTED=$$(gofmt -l .); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "Go formatting issues found. Run 'make fmt' to fix:"; \
		echo "$$UNFORMATTED"; \
		exit 1; \
	fi
	@echo "Go formatting OK."

## ci: Run fast CI checks (format check, vet, compatibility guardrails, tests, build)
ci: fmt-check lint compat-literals dockerfile-stages test-fast build
	@echo ""
	@echo "CI passed."

## ci-full: Run the full CI pipeline locally (mirrors GitHub Actions, includes web + golangci-lint)
ci-full: fmt-check web web-typecheck web-test lint compat-literals dockerfile-stages golangci-lint test-fast build
	@echo ""
	@echo "CI (full) passed."

## proto: Generate Go code from .proto files
proto:
	@echo "Generating protobuf Go code..."
	@protoc \
		--proto_path=proto \
		--go_out=. --go_opt=module=github.com/GoogleCloudPlatform/scion \
		--go-grpc_out=. --go-grpc_opt=module=github.com/GoogleCloudPlatform/scion \
		proto/broker/v1/broker.proto
	@echo "Proto generation done."

## proto-check: Verify generated protobuf code is up to date
proto-check:
	@echo "Checking protobuf generated code is up to date..."
	@TMP=$$(mktemp -d) && \
	protoc \
		--proto_path=proto \
		--go_out=$$TMP --go_opt=module=github.com/GoogleCloudPlatform/scion \
		--go-grpc_out=$$TMP --go-grpc_opt=module=github.com/GoogleCloudPlatform/scion \
		proto/broker/v1/broker.proto && \
	diff $$TMP/proto/broker/v1/broker.pb.go proto/broker/v1/broker.pb.go && \
	diff $$TMP/proto/broker/v1/broker_grpc.pb.go proto/broker/v1/broker_grpc.pb.go && \
	rm -rf $$TMP && \
	echo "Proto generated code is up to date." || \
	(rm -rf $$TMP; echo "Proto generated code is out of date. Run 'make proto' to regenerate."; exit 1)

## clean: Remove build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR) .build web/dist
	@mkdir -p web/dist/client && touch web/dist/client/.gitkeep
	@rm -f $(BINARY)
	@echo "Done."

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | column -t -s ':'
