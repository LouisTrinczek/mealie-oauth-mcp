# Contributing

Thanks for helping. This guide covers setup, tests, conventions, and how to
add a tool. Please read the [tool stability policy](#tool-stability-policy)
before you change an existing tool.

## Getting started

You need Go 1.27 or later and Docker.

```sh
git clone https://github.com/LouisTrinczek/mealie-oauth-mcp.git
cd mealie-oauth-mcp
make setup
```

`make setup` enables the pre-commit hook. It rejects commits with unformatted
Go files.

## Development environment

`make dev-up` starts Mealie, a mock OIDC provider, and the server from your
working tree, all in Docker. The first run builds the image and pulls Mealie,
so it takes a few minutes.

```sh
make dev-up
```

Then:

- Open Mealie at `http://localhost:9000`.
- Connect an MCP client to `http://localhost:8080/mcp`.

The login is automatic. The mock provider signs you in as
`it-user@example.com`, so there is no password to enter.

`make dev-up` creates `dev/encryption_keys.txt` once. `make dev-down` keeps
it, so your next `make dev-up` uses the same key.

Other commands:

```sh
make dev-check   # run the dev environment test against the running stack
make dev-down    # stop everything and delete its data
```

Ports on the host, all bound to `127.0.0.1`:

| Port | Service |
|---|---|
| 9000 | Mealie |
| 8080 | MCP server |
| 8180 | Mock OIDC provider |
| 9090 | Metrics |

If one of these ports is taken, stop the program that uses it and run
`make dev-up` again. Don't remap the OIDC port: the issuer URL contains the
port, so the host port and the container port must match. If you remap another
port in `dev/compose.yml`, update the matching URL setting too.

Safari, and some other browsers, don't resolve `oidc.localhost` to your
machine. Add this line to your hosts file if the login redirect fails:

```
127.0.0.1 oidc.localhost
```

## Tests

| Command | What it runs |
|---|---|
| `make test` | Unit tests with the race detector. Writes `coverage.out`. |
| `make cover` | `make test`, then the per-package coverage minimums in `.coverage-thresholds`. The default minimum is 100% per package. |
| `make test-integration` | End-to-end test against real Mealie and a mock OIDC provider, in Docker. |
| `make fuzz` | Every fuzz target for `FUZZTIME` each (default 10s), for example `make fuzz FUZZTIME=1m`. |
| `make dev-check` | The dev environment test. Needs `make dev-up` first. |
| `make release-dry-run` | A GoReleaser snapshot build in Docker. Nothing is published. |

Run `go test -race -run TestName ./internal/<pkg>/` for a single test.

Before you open a pull request, run these and fix any failure:

```sh
gofmt -l .        # no output expected
go vet ./...
make lint
make cover
```

Tests that compare output to a golden file live in `testdata/` next to the
code. After an intended change, refresh them and review the diff before you
commit:

```sh
go test ./internal/<pkg>/ -update
```

## Coding conventions

- No code comments. Explain through names, types, and small functions. If
  something needs a comment, rename or restructure it. Directives such as
  `//go:build` and `//go:embed` are fine, and `//nolint` is not: fix the
  finding.
- Write the minimum code that solves the problem. No speculative options,
  hooks, or abstractions.
- Follow the patterns already in the project before you add a new one.
- Touch only what you must. Clean up only what you changed.
- One package, file, and function per concern. Name packages by what they
  provide, never `util` or `common`.
- Standard library first. Ask before you add a dependency.
- The binary must build with `CGO_ENABLED=0`.
- `context.Context` is the first parameter of anything that does I/O. Wrap
  errors with `fmt.Errorf("doing x: %w", err)`. No `panic` outside `main` and
  tests.
- Inject dependencies (clock, HTTP client, store) through constructors. No
  package-level mutable state.
- Tests are table-driven where that helps, use `httptest` for HTTP, and live
  next to the code.

Security rules. A change that breaks one is wrong even if tests pass:

- Never log or return tokens, auth codes, PKCE verifiers, or `Authorization`
  headers. That includes error messages and tool output. Mealie error bodies
  go through redaction before an agent sees them.
- `PUBLIC_URL` comes from config only, never from request headers.
- No tool and no `mealie_api` path may reach Mealie admin, auth, API-token,
  user-management, webhook, or notification endpoints. The `mealie_api`
  allowlist fails closed, and paths are normalized before matching.
- MCP tokens are opaque random values stored only as SHA-256 hashes. Mealie
  tokens are stored AES-GCM encrypted.
- Every security rule gets a test, including bypass attempts.

Mealie ships every one to two weeks and adds fields freely:

- Decode Mealie responses leniently. Unknown fields must never cause errors.
- Never update a Mealie object by round-tripping it through a typed struct.
  Use `PATCH` with only the changed fields, or `GET` the raw JSON, merge, and
  `PUT` it back, so unknown fields survive.

## Commits and pull requests

- Write commit messages in the
  [Conventional Commits](https://www.conventionalcommits.org) format, for
  example `fix(oauth): reject empty redirect URIs`. Keep the subject to one
  sentence with no trailing period.
- Sign off every commit with `git commit -s`. CI checks it. The sign-off
  certifies that you wrote the change, or have the right to submit it, under
  the project's open-source license. See the
  [Developer Certificate of Origin](https://developercertificate.org/).
- Keep one topic per pull request.
- CI must be green, including the SonarQube quality gate.
- `main` requires a pull request, 1 approving review and passing `lint`,
  `test`, `dco` and `sonar` checks, and blocks force pushes and deletion.
  The repo admin merges release-please pull requests through the ruleset
  bypass.
- Fill in the checklist in the pull request template.

## Adding a tool

Tools are grouped by area and split into a `_read` and a `_write` tool. Each
action is one entry in that tool's list. If your change fits an existing
tool, add an action to it. Add a new tool only for a new area.

1. Pick the area file in `internal/mcpserver/` (for example `recipes.go`), or
   add a new one for a new area. Register a new tool in `server.go`.
2. Define the input struct and the actions with `addActionTool`. The struct
   embeds `actionField` and each field's `jsonschema` tag says which actions
   use it. Each action has a `name`, a `help` line that lists its required and
   optional arguments, and a `run` function. Set `read: true` in the
   `toolSpec` for a tool that only reads.
3. Shape the output with `internal/view`, so lists are compact and `get`
   results drop null and empty values. Offer a `full` argument when an agent
   may need the complete object.
4. Map errors so an agent sees what failed and what to try next. Return
   errors through the session's `failure` path so Mealie error bodies are
   redacted and status codes get a hint.
5. Add table tests that use the fake Mealie in `harness_test.go`, and an
   output golden file with `assertGolden`.
6. Run `go test ./internal/mcpserver/ -update` to refresh
   `testdata/tool_surface.golden.json`, and review the diff.
7. If the tool writes data, add a step to the integration tests in
   `test/integration/`.
8. Add the tool and its actions to the Tools table in the README.
9. If the tool needs a Mealie endpoint that `mealie_api` doesn't allow yet,
   extend the allowlist in `internal/allowlist/` with tests, including bypass
   attempts. Never allow admin, auth, API-token, user-management, webhook, or
   notification endpoints.

## Tool stability policy

Tool names, action names, and argument meanings are a public interface. A
breaking change needs a major version, or a deprecation period. During the
deprecation period the old name still works and its description says what
replaces it.

These changes are breaking:

- Removing or renaming a tool, an action, or an argument.
- Changing an argument's type or meaning.
- Making an optional argument required.

These changes are not breaking:

- Adding a tool, an action, or an optional argument.
- Rewording descriptions.

`internal/mcpserver/testdata/tool_surface.golden.json` records every tool,
action, and argument, without descriptions. In review, a changed or removed
existing entry in this file signals a possible break. Added entries are
normally fine.

## Releases

[release-please](https://github.com/googleapis/release-please) reads the
Conventional Commits on `main` and keeps a release pull request open. It
updates `CHANGELOG.md` and the version in that pull request. The repository
admin merges it through the `main` ruleset bypass. Release pull requests are
opened with `GITHUB_TOKEN`, so CI doesn't run on them.

Merging the release pull request creates the tag and the GitHub release. The
release workflow then publishes the multi-arch image to
`ghcr.io/louistrinczek/mealie-oauth-mcp`, attaches binary archives and signed
checksums to the release, and adds the SBOM and build attestation. Use
`make release-dry-run` to check the build without publishing.

## Reporting security issues

Don't open a public issue for a vulnerability. Follow [SECURITY.md](SECURITY.md).
