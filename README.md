# mealie-oauth-mcp

A remote [MCP](https://modelcontextprotocol.io) server for
[Mealie](https://mealie.io) that lets AI agents (Claude.ai, Claude Code, and
other MCP clients) work with your recipes, meal plans, and shopping lists.

Unlike servers that hold one static Mealie API token behind an open URL, this
server requires an OIDC login through the identity provider Mealie already
uses. Each connected agent acts as the Mealie user who logged in, with that
user's permissions.

> **Status:** early development. Nothing here is usable yet.

## Planned features

- OAuth 2.1 authorization server following the MCP authorization spec
  (2025-11-25), with Dynamic Client Registration and Client ID Metadata
  Documents
- Login delegated to Mealie's native OIDC flow; works with any OIDC provider
  Mealie supports (Zitadel, Authentik, Keycloak, Authelia, Pocket ID, Google,
  Entra ID, …)
- Per-user sessions; agents never see a Mealie token
- Tools for everyday Mealie features: recipes, meal planner, history,
  cookbooks, organizers, shopping lists, foods, units, labels, comments,
  ratings, ingredient parsing, and recipe import
- Admin endpoints are unreachable by design
- One static binary in a multi-arch Docker image

## Requirements

- Mealie **v3.23.0 or later** with OIDC enabled
- The server's callback URL (`<PUBLIC_URL>/callback`) registered as an allowed
  redirect URI on Mealie's OIDC client in your identity provider
- A reverse proxy that terminates TLS

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `MEALIE_URL` | yes | | Mealie base URL |
| `PUBLIC_URL` | yes | | This server's public HTTPS origin |
| `ENCRYPTION_KEYS` | yes | | Comma-separated `id:base64key` list of 32-byte keys; the first encrypts, all decrypt |
| `DATA_DIR` | no | `/data` | SQLite location |
| `LISTEN_ADDR` | no | `:8080` | Listen address |
| `SESSION_MAX_AGE` | no | `720h` | Absolute session cap |
| `ACCESS_TOKEN_TTL` | no | `1h` | MCP access token lifetime |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

Generate an encryption key with:

```sh
openssl rand -base64 32
```

Sessions end after `SESSION_MAX_AGE`, or earlier if unused for longer than
Mealie's `TOKEN_TIME` (48 h by default).

## Development

Requires Go 1.27 or later.

```sh
go build ./...          # build
go test -race ./...     # run tests
go vet ./...            # static checks
gofmt -l .              # list unformatted files
go run ./cmd/mealie-oauth-mcp
```

### Project layout

```
cmd/mealie-oauth-mcp/   main package: config, wiring, startup
internal/               server packages (not importable by other modules)
```

Packages under `internal/` are added as the code needs them. The expected
split is OAuth server, Mealie API client, MCP tools, storage, and config.

## License

MIT (license file to be added).
