BINARY := fleet
BUILD_DIR := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

.PHONY: build run clean test fmt install lint coverage deps vet setup

build:
	go build -v -ldflags "-s -w -X main.version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY) ./cmd/fleet

# Build then exec (not `go run`): fleet writes its own path into Claude's
# hooks, and go run deletes its binary on exit, leaving them dangling.
# version=dev keeps the auto-updater from swapping in the released binary.
run:
	go build -ldflags "-X main.version=dev" -o $(BUILD_DIR)/$(BINARY) ./cmd/fleet
	FLEET_DEBUG=$${FLEET_DEBUG:-1} ./$(BUILD_DIR)/$(BINARY)

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
