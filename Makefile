SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
DEADCODE := golang.org/x/tools/cmd/deadcode@v0.52.0
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.10.0

.PHONY: help build install fmt vet test check lint security

help: ## List available targets
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_-]+:.*## / { printf "  make %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build modguard binary into ./bin/modguard
	GOWORK=off go build -o ./bin/modguard ./cmd/modguard

install: ## Install modguard into GOBIN / GOPATH/bin
	GOWORK=off go install ./cmd/modguard

fmt: ## Format Go source files
	gofmt -w .

vet: ## Run go vet
	GOWORK=off go vet ./...

test: ## Run unit and integration tests with race detector
	GOWORK=off go test -race -count=1 ./...

lint: ## Run staticcheck and deadcode
	GOWORK=off go run $(STATICCHECK) -checks=all ./...
	@out=$$(GOWORK=off go run $(DEADCODE) ./...); if [[ -n "$$out" ]]; then echo "$$out" >&2; exit 1; fi

security: ## Run govulncheck
	GOWORK=off go run $(GOVULNCHECK) ./...

check: ## Full verification (format check, vet, tests, self-dogfooding)
	@unformatted=$$(gofmt -l .); if [[ -n "$$unformatted" ]]; then echo "Run make fmt:"; echo "$$unformatted"; exit 1; fi
	$(MAKE) vet
	$(MAKE) test
	GOWORK=off go run ./cmd/modguard check --level strict --with-tools .
	@if [[ "$${LINT:-0}" == "1" ]]; then $(MAKE) lint; fi
