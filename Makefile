BINARY := fleet
BUILD_DIR := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

.PHONY: build run clean test fmt install lint coverage deps vet setup

build:
	go build -v -ldflags "-s -w -X main.version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY) ./cmd/fleet

run:
	go run ./cmd/fleet

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
	install -m 755 $(BUILD_DIR)/$(BINARY) ~/.local/bin/$(BINARY)

setup:
	pre-commit install
