BINARY  := council
PKG     := ./cmd/council
GOFLAGS ?= -mod=readonly

# Two test binaries with eight procs each cap tests at 16 cores; the defaults
# saturate the 64-core dev host.
TEST_ENV := GOMAXPROCS=8
TEST_JOBS := -p 2

.PHONY: help fmt fmt-check vet build test test-race cover lint ci bootstrap

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

fmt: ## Format all Go files in place
	gofmt -w .

fmt-check: ## Fail if any Go file is not gofmt-clean
	@test -z "$$(gofmt -l .)" || { echo "not gofmt-clean:"; gofmt -l .; exit 1; }

vet: ## Run go vet
	go vet ./...

build: ## Build the council binary into bin/
	go build -o bin/$(BINARY) $(PKG)

test: ## Run the test suite
	$(TEST_ENV) go test $(TEST_JOBS) ./... -count=1

test-race: ## Run the test suite with the race detector
	$(TEST_ENV) go test $(TEST_JOBS) ./... -race -count=1

cover: ## Run tests with a coverage profile and print the total
	$(TEST_ENV) go test $(TEST_JOBS) ./... -count=1 -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -n 1

lint: ## Run golangci-lint if installed (otherwise skip)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping lint"; \
		echo "install: https://golangci-lint.run/docs/welcome/install/"; \
	fi

ci: fmt-check vet build test-race ## What CI runs (excluding coverage + lint)

# First-run bootstrap from README.md: create the store, then load the shipped
# pack. Network-dependent harness steps (cases load, graders calibrate, harness
# weekly) and the long-running `council serve` are intentionally NOT run here —
# see README.md "Bootstrap".
bootstrap: ## Bootstrap the store + shipped pack (go run)
	mkdir -p data # data/ is gitignored and absent in a fresh clone
	go run $(PKG) db migrate
	go run $(PKG) pack init --file config/pack.yaml
	@echo "store ready; next: go run $(PKG) serve  (or 'make build && ./bin/$(BINARY) serve')"
