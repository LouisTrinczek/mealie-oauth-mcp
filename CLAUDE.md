# mealie-oauth-mcp

A remote MCP server (Go) that gives AI agents access to a self-hosted Mealie
instance. Users log in through Mealie's own OIDC flow, and every action runs as
that Mealie user. The design spec is the source of truth for behavior:
`docs/superpowers/specs/2026-10-28-mealie-mcp-design.md`. Read the relevant
section before working on auth, tools, or storage. If the code and the spec
disagree, say so instead of silently picking one.

## Coding rules

- No code comments. Code must explain itself through names, types, and small
  functions. If something needs a comment, rename or restructure it instead.
  Directives are not comments and stay: `//go:build`, `//go:embed`, and
  goose's `-- +goose Up`. No `//nolint`; fix the finding instead. Config
  files (workflows, compose, Dockerfile) may carry a short note where one is
  needed.
- YAGNI, KISS, DRY: write only the minimum code that solves the problem at
  hand. No speculative options, hooks, or abstractions.
- Follow existing project patterns. Reuse what the codebase already does
  before introducing a new pattern.
- Touch only what you must. Clean up only what you changed; leave unrelated
  code alone.
- Never commit or push without explicit user approval.
- Keep responsibilities separated: one package, file, and function per
  concern.

## Commands

- Build: `go build ./...`
- Test: `go test -race ./...`
- Single test: `go test -race -run TestName ./internal/<pkg>/`
- Vet: `go vet ./...`
- Format: `gofmt -l -w .`
- Tidy modules: `go mod tidy`

Before calling a change done, run `gofmt -l .` (no output expected),
`go vet ./...`, and `go test -race ./...`, and report the result.

Code must be formatted before every commit. The `.githooks/pre-commit` hook
rejects commits with unformatted Go files; enable it once per clone with
`git config core.hooksPath .githooks`. Never bypass it with `--no-verify`.

## Layout

- `cmd/mealie-oauth-mcp/`: `main` only. Parse config, wire dependencies, call
  `run(ctx)`. No business logic.
- `internal/`: all other packages. There is no `pkg/`; nothing here is a
  public library.
- Create a package when there is code for it, not ahead of time. Name packages
  by what they provide (`oauth`, `mealie`, `store`), never `util` or `common`.

## Non-negotiable security rules

These come from the spec's anti-goals. A change that breaks one is wrong even if
tests pass.

- Never log or return tokens, auth codes, PKCE verifiers, or `Authorization`
  headers. That includes error messages and tool output. Mealie error bodies
  go through redaction before an agent sees them.
- `PUBLIC_URL` comes from config only, never from request headers.
- No tool and no `mealie_api` path may reach Mealie admin, auth, API-token,
  user-management, webhook, or notification endpoints. The `mealie_api`
  escape hatch is an allowlist that fails closed; normalize paths before
  matching.
- MCP tokens are opaque random values, stored only as SHA-256 hashes. Mealie
  tokens are stored AES-GCM encrypted.
- Every security rule above gets a test, including bypass attempts.

## Mealie compatibility

Mealie ships every one to two weeks and adds fields freely.

- Decode Mealie responses leniently; unknown fields must never cause errors.
- Never update a Mealie object by round-tripping it through a typed struct.
  Use `PATCH` with only the changed fields, or `GET` the raw JSON, merge, and
  `PUT` it back, so unknown fields survive.

## Go conventions for this repo

- Standard library first: `net/http` for routing, `log/slog` for logging,
  `crypto/*` for crypto. Ask before adding a dependency; the MCP SDK
  (`github.com/modelcontextprotocol/go-sdk`) and a pure-Go SQLite driver are
  already agreed.
- The binary must build with `CGO_ENABLED=0` (static image, multi-arch).
- `context.Context` is the first parameter of anything that does I/O.
- Wrap errors with `fmt.Errorf("doing x: %w", err)`; no `panic` outside
  `main` and tests.
- Inject dependencies (clock, HTTP client, store) through constructors so tests
  can replace them. No package-level mutable state.
- Tests are table-driven where it helps, use `httptest` for HTTP, and live next
  to the code (`foo_test.go`). Integration tests that need Docker use the
  `integration` build tag.

## Tool output for agents

Tool responses are read by an LLM. Keep list results compact (id, slug, name,
a few useful fields, pagination info) and drop nulls and empty values from
`get` results. Errors say what failed and what to try next.
