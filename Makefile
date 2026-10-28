FUZZTIME ?= 30s
INTEGRATION_COMPOSE = docker compose -f test/integration/compose.yml

.PHONY: setup build test cover lint vuln fuzz test-integration

setup:
	git config core.hooksPath .githooks

build:
	CGO_ENABLED=0 go build -trimpath ./...

test:
	go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...

cover: test
	sh scripts/coverage-check.sh coverage.out .coverage-thresholds

lint:
	go tool golangci-lint run

vuln:
	go tool govulncheck ./...

fuzz:
	go test -run='^$$' -fuzz='^FuzzOpen$$' -fuzztime=10s ./internal/tokencrypt/

test-integration:
	$(INTEGRATION_COMPOSE) run --rm --build it
	$(INTEGRATION_COMPOSE) down -v
