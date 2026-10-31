FUZZTIME ?= 10s
GORELEASER_VERSION ?= v2.19.0
INTEGRATION_COMPOSE = docker compose -f test/integration/compose.yml
DEV_COMPOSE = docker compose -f dev/compose.yml
DEV_KEYS = dev/encryption_keys.txt

.PHONY: setup build test cover lint vuln fuzz test-integration dev-up dev-down dev-check release-dry-run

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
	go test -run='^$$' -fuzz='^FuzzOpen$$' -fuzztime=$(FUZZTIME) ./internal/tokencrypt/
	go test -run='^$$' -fuzz='^FuzzMatchRedirectURI$$' -fuzztime=$(FUZZTIME) ./internal/oauth/
	go test -run='^$$' -fuzz='^FuzzValidateRedirectURI$$' -fuzztime=$(FUZZTIME) ./internal/oauth/
	go test -run='^$$' -fuzz='^FuzzCIMDParse$$' -fuzztime=$(FUZZTIME) ./internal/oauth/
	go test -run='^$$' -fuzz='^FuzzCheck$$' -fuzztime=$(FUZZTIME) ./internal/allowlist/
	go test -run='^$$' -fuzz='^FuzzPathOf$$' -fuzztime=$(FUZZTIME) ./internal/mealie/

test-integration:
	$(INTEGRATION_COMPOSE) run --rm --build it; status=$$?; $(INTEGRATION_COMPOSE) down -v; exit $$status

dev-up: $(DEV_KEYS)
	$(DEV_COMPOSE) up -d --build --wait || { $(DEV_COMPOSE) down; exit 1; }

$(DEV_KEYS):
	key=$$(openssl rand -base64 32) && printf 'dev:%s\n' "$$key" > $@
	chmod 644 $@

dev-down:
	$(DEV_COMPOSE) down -v

dev-check:
	go test -tags devenv -count=1 ./test/integration/ -run TestDevEnvironment

release-dry-run:
	docker run --rm -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src -v "$(CURDIR)":/src -w /src goreleaser/goreleaser:$(GORELEASER_VERSION) release --snapshot --clean --skip=sign,sbom
