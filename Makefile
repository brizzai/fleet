BINARY := fleet
BUILD_DIR := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

.PHONY: build run clean test fmt install lint coverage deps vet setup demo demo-setup demo-clean

DEMO_PREFIX := /tmp/fleet-demo

build:
	go build -v -ldflags "-s -w -X main.version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY) ./cmd/fleet

run:
	FLEET_DEBUG=$${FLEET_DEBUG:-1} go run ./cmd/fleet

clean:
	rm -rf $(BUILD_DIR)
	go clean

test:
	go test -race -v ./...

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

COVERAGE_EXCLUDE := /(ui|cmd|chrome|debuglog|diagnostics|update)/

coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	@echo "\n--- All packages ---"
	@go tool cover -func=coverage.out | tail -1
	@grep -v -E '$(COVERAGE_EXCLUDE)' coverage.out > coverage-core.out
	@echo "--- Core packages (excl. UI, CLI, infra) ---"
	@go tool cover -func=coverage-core.out | tail -1

deps:
	go mod download

vet:
	go vet ./...

install: build
	cp $(BUILD_DIR)/$(BINARY) ~/.local/bin/

setup:
	pre-commit install

# --- Demo / screenshot mode -------------------------------------------------
# demo-setup builds a throwaway fleet under $(DEMO_PREFIX): fake repos, one
# worktree grouped under its origin, and 5 sessions. `make demo` then launches
# fleet filtered to just those repos (FLEET_DEMO_PREFIX) with the fake gh on
# PATH so PR badges render. `make demo-clean` tears it all down.
#   Typical flow:  make demo-setup   (wait ~30s)   make demo
demo-setup:
	bash demo/setup.sh

demo: build
	FLEET_DEMO_PREFIX=$(DEMO_PREFIX) PATH="$(CURDIR)/demo:$$PATH" $(BUILD_DIR)/$(BINARY)

demo-clean:
	bash demo/cleanup.sh
