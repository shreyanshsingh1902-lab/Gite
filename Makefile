# ========================================================
# gite Makefile - Build & Development Automation
# ========================================================

BINARY_NAME := gite
BIN_DIR     := bin
SRC_MAIN    := ./cmd/gite
PKG_ALL     := ./...

.PHONY: all build run test fmt vet lint clean migrate help

all: build

## build: Compile the gite standalone binary
build:
	@mkdir -p $(BIN_DIR)
	@echo "==> Building $(BINARY_NAME)..."
	go build -ldflags="-s -w" -o $(BIN_DIR)/$(BINARY_NAME) $(SRC_MAIN)
	@echo "==> Binary built at $(BIN_DIR)/$(BINARY_NAME)"

## run: Run the gite forge server locally
run:
	@echo "==> Starting $(BINARY_NAME) server..."
	go run $(SRC_MAIN) serve

## migrate: Initialize or upgrade the SQLite database schema
migrate:
	@echo "==> Running migrations..."
	go run $(SRC_MAIN) migrate

## test: Run unit and integration tests with race detector
test:
	@echo "==> Running tests..."
	go test -v -race -timeout 60s $(PKG_ALL)

## fmt: Format all Go source files according to Go standards
fmt:
	@echo "==> Formatting code..."
	gofmt -s -w .

## vet: Run Go static analysis (go vet)
vet:
	@echo "==> Running go vet..."
	go vet $(PKG_ALL)

## lint: Check formatting and run vet
lint: fmt vet
	@echo "==> Linting checks passed."

## clean: Remove compiled binaries and build artifacts
clean:
	@echo "==> Cleaning build artifacts..."
	rm -rf $(BIN_DIR) coverage.out

## help: Display available make targets
help:
	@echo "Available commands in gite Makefile:"
	@echo ""
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'
