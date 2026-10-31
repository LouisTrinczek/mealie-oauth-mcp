FUZZTIME ?= 30s
GORELEASER_VERSION ?= v2.19.0
INTEGRATION_COMPOSE = docker compose -f test/integration/compose.yml

.PHONY: setup build test cover lint vuln fuzz test-integration release-dry-run

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
	go test -run='^$$' -fuzz='^FuzzMatchRedirectURI$$' -fuzztime=10s ./internal/oauth/
	go test -run='^$$' -fuzz='^FuzzValidateRedirectURI$$' -fuzztime=10s ./internal/oauth/
	go test -run='^$$' -fuzz='^FuzzCIMDParse$$' -fuzztime=10s ./internal/oauth/
	go test -run='^$$' -fuzz='^FuzzCheck$$' -fuzztime=10s ./internal/allowlist/
	go test -run='^$$' -fuzz='^FuzzPathOf$$' -fuzztime=10s ./internal/mealie/

test-integration:
	$(INTEGRATION_COMPOSE) run --rm --build it; status=$$?; $(INTEGRATION_COMPOSE) down -v; exit $$status

release-dry-run:
	docker run --rm -v "$(CURDIR)":/src -w /src goreleaser/goreleaser:$(GORELEASER_VERSION) release --snapshot --clean --skip=sign,sbom
