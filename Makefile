# Redis Stream Client Go - Makefile
# Common development tasks and build automation

.PHONY: help build test test-integration test-unit test-coverage clean lint fmt vet deps deps-update security docker-build docker-test benchmark examples

# Default target
.DEFAULT_GOAL := help

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=gofmt
GOVET=$(GOCMD) vet

# Build parameters
BINARY_NAME=redis-stream-client
BINARY_PATH=./bin/$(BINARY_NAME)
MAIN_PATH=./cmd/$(BINARY_NAME)

# Test parameters
COVERAGE_FILE=coverage.out
COVERAGE_HTML=coverage.html

# Docker parameters
DOCKER_IMAGE=redis-stream-client-go
DOCKER_TAG=latest

## help: Display this help message
help:
	@echo "Redis Stream Client Go - Development Commands"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ { printf "  %-20s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

## build: Build the application binary
build:
	@echo "Building binary..."
	@mkdir -p bin
	$(GOBUILD) -o $(BINARY_PATH) -v $(MAIN_PATH)

## clean: Clean build artifacts and temporary files
clean:
	@echo "Cleaning build artifacts..."
	$(GOCLEAN)
	@rm -rf bin/
	@rm -f $(COVERAGE_FILE) $(COVERAGE_HTML)
	@rm -f profile.cov

## test: Run all tests
test: test-unit test-integration

## test-unit: Run unit tests only
test-unit:
	@echo "Running unit tests..."
	$(GOTEST) -v -short ./...

## test-integration: Run integration tests only
test-integration:
	@echo "Running integration tests..."
	$(GOTEST) -v ./test/...

## test-coverage: Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	$(GOTEST) -coverprofile=$(COVERAGE_FILE) ./...
	$(GOCMD) tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML)
	@echo "Coverage report generated: $(COVERAGE_HTML)"

## test-coverage-func: Show test coverage by function
test-coverage-func:
	@echo "Running tests with function coverage..."
	$(GOTEST) -coverprofile=$(COVERAGE_FILE) ./...
	$(GOCMD) tool cover -func=$(COVERAGE_FILE)

## benchmark: Run benchmark tests
benchmark:
	@echo "Running benchmark tests..."
	$(GOTEST) -bench=. -benchmem ./...

## lint: Run golangci-lint (requires golangci-lint to be installed)
lint:
	@echo "Running golangci-lint..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --issues-exit-code=1; \
	else \
		echo "golangci-lint not installed. Install with: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$$(go env GOPATH)/bin v1.54.2"; \
	fi

## fmt: Format Go code
fmt:
	@echo "Formatting Go code..."
	$(GOFMT) -s -w .

## vet: Run go vet
vet:
	@echo "Running go vet..."
	$(GOVET) ./...

## deps: Download and verify dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) verify

## deps-update: Update dependencies
deps-update:
	@echo "Updating dependencies..."
	$(GOMOD) tidy
	$(GOGET) -u ./...
	$(GOMOD) tidy

## deps-vendor: Create vendor directory
deps-vendor:
	@echo "Creating vendor directory..."
	$(GOMOD) vendor

## security: Run security checks (requires gosec)
security:
	@echo "Running security checks..."
	@if command -v gosec >/dev/null 2>&1; then \
		gosec ./...; \
	else \
		echo "gosec not installed. Security scanning skipped."; \
		echo "Install gosec manually if needed for security scanning."; \
	fi

## docker-build: Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

## docker-test: Run tests in Docker container
docker-test:
	@echo "Running tests in Docker..."
	docker run --rm -v $(PWD):/app -w /app golang:1.23 make test

## examples: Run example applications
examples:
	@echo "Running examples..."
	@if [ -d "examples" ]; then \
		for example in examples/*/; do \
			echo "Running example: $$example"; \
			cd "$$example" && go run . && cd ../..; \
		done \
	else \
		echo "No examples directory found"; \
	fi

## install-tools: Install development tools
install-tools:
	@echo "Installing development tools..."
	@echo "Installing golangci-lint..."
	@curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin v1.54.2
	@echo "Installing gosec..."
	@echo "Note: gosec installation skipped - install manually if needed"
	@echo "Installing goimports..."
	@go install golang.org/x/tools/cmd/goimports@latest

## check: Run all quality checks
check: fmt vet lint test-coverage security
	@echo "All quality checks completed!"

## ci: Run CI pipeline locally
ci: deps check
	@echo "CI pipeline completed successfully!"

## dev-setup: Set up development environment
dev-setup: deps install-tools
	@echo "Development environment setup completed!"
	@echo ""
	@echo "Next steps:"
	@echo "1. Set environment variables: export POD_NAME=test-consumer"
	@echo "2. Run tests: make test"
	@echo "3. Start developing!"

## release-check: Verify release readiness
release-check: clean deps check
	@echo "Release readiness check completed!"

# Development helpers
.PHONY: watch
## watch: Watch for file changes and run tests (requires entr)
watch:
	@if command -v entr >/dev/null 2>&1; then \
		find . -name '*.go' | entr -c make test-unit; \
	else \
		echo "entr not installed. Install with your package manager (e.g., brew install entr)"; \
	fi
