# mealie-oauth-mcp

[![Quality Gate Status](https://sonarqube.lt-ds.de/api/project_badges/measure?project=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977&metric=alert_status)](https://sonarqube.lt-ds.de/dashboard?id=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977)
[![Coverage](https://sonarqube.lt-ds.de/api/project_badges/measure?project=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977&metric=coverage)](https://sonarqube.lt-ds.de/dashboard?id=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977)
[![Security Rating](https://sonarqube.lt-ds.de/api/project_badges/measure?project=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977&metric=security_rating)](https://sonarqube.lt-ds.de/dashboard?id=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977)
[![Reliability Rating](https://sonarqube.lt-ds.de/api/project_badges/measure?project=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977&metric=reliability_rating)](https://sonarqube.lt-ds.de/dashboard?id=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977)
[![Maintainability Rating](https://sonarqube.lt-ds.de/api/project_badges/measure?project=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977&metric=sqale_rating)](https://sonarqube.lt-ds.de/dashboard?id=LouisTrinczek_mealie-oauth-mcp_72e5801c-dbee-4707-95f1-fd836014f977)

A remote [MCP](https://modelcontextprotocol.io) server for
[Mealie](https://mealie.io) that lets AI agents (Claude.ai, Claude Code, and
other MCP clients) work with your recipes, meal plans, and shopping lists.

Unlike servers that hold one static Mealie API token behind an open URL, this
server requires an OIDC login through the identity provider Mealie already
uses. Each connected agent acts as the Mealie user who logged in, with that
user's permissions.

> **Status:** Login, `whoami` and the Mealie tools are working.

## Features

- OAuth 2.1 authorization server following the MCP authorization spec
  (2025-11-25), with Dynamic Client Registration and Client ID Metadata
  Documents
- Login delegated to Mealie's native OIDC flow; works with any OIDC provider
  Mealie supports
- Per-user sessions; agents never see a Mealie token
- Admin endpoints are unreachable by design
- One static binary in a multi-arch Docker image

## Requirements

- Mealie **v3.25.0 or later** with OIDC enabled. Earlier versions do not
  return `expires_in` from the native token endpoints and expose token refresh
  as GET; the server refuses to start against them.
- The server's callback URL (`<PUBLIC_URL>/callback`) registered as an allowed
  redirect URI on Mealie's OIDC client in your identity provider
- A reverse proxy that terminates TLS

## Deployment

An example Compose file is in
[`deploy/docker-compose.example.yml`](deploy/docker-compose.example.yml) and
an environment template in [`.env.example`](.env.example). The container runs
as a non-root user, needs only a writable `/data` volume, and publishes no
port: put a reverse proxy that terminates TLS in front of it and forward to
port 8080.

Set `TRUSTED_PROXIES` to the address range your reverse proxy connects from,
for example the Docker network's subnet (`docker network inspect <network>`
shows it). The OAuth endpoints (`/register`, `/authorize`, `/callback`,
`/token`, `/revoke`) allow 30 requests per minute per client address. Without
`TRUSTED_PROXIES` the server sees every request as coming from the proxy, so
all users share one budget, and one request every two seconds from anyone is
enough to block every login and token refresh. With it, the server takes the
client address from the proxy's `X-Forwarded-For` header. List only your own
proxies: a client inside a trusted range can claim any address. The server
logs a warning at startup when the variable is empty.

The server answers these routes:

| Route | Purpose |
|---|---|
| `/mcp` | MCP endpoint (needs a bearer token) |
| `/authorize`, `/callback`, `/consent`, `/token`, `/register`, `/revoke` | OAuth 2.1 endpoints |
| `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource` | OAuth metadata |
| `/healthz` | Liveness |
| `/readyz` | Readiness: database and Mealie reachable |

At startup the server checks that Mealie is reachable (retrying for up to 30
seconds), has OIDC enabled, is at least v3.25.0, and serves its native OIDC
config. It exits with an error otherwise.

On stop, in-flight requests get 5 seconds to finish, then open streams are
closed. This fits Docker's default 10 second stop grace period.

### Reverse proxy on the same host

The MCP SDK's DNS-rebinding protection is on for `/mcp`. When a request
arrives over a loopback connection (`127.0.0.1` or `::1`) and its `Host`
header is not a loopback host, the server answers `403`. The check looks at
the connection, not at `LISTEN_ADDR`, so changing `LISTEN_ADDR` does not help.
The OAuth endpoints are not affected.

- **Docker (recommended):** unaffected. A proxy in another container, or a
  host proxy going through the published port, reaches the container over its
  bridge-network address, which is not loopback.
- **Binary run directly with a proxy on the same machine:** requests are
  rejected if the proxy connects over `127.0.0.1` and forwards the public
  `Host` header. Caddy forwards it by default, and so does nginx with
  `proxy_set_header Host $host`. Either connect the proxy to the server's
  non-loopback address, or do not rewrite `Host`. nginx's default
  `proxy_pass http://127.0.0.1:8080` sends `Host: 127.0.0.1:8080` and passes.

## Configuration

Every variable can be set in the environment. Empty values count as unset.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `MEALIE_URL` | yes | | Mealie base URL (`http` or `https`, no query or fragment) |
| `PUBLIC_URL` | yes | | This server's public HTTPS origin, without a path |
| `ENCRYPTION_KEYS` | yes, unless the `_FILE` form is used | | Comma-separated `id:base64key` list of 32-byte keys; the first encrypts, all decrypt. Key IDs are 1 to 32 characters of `A-Z a-z 0-9 _ -` |
| `ENCRYPTION_KEYS_FILE` | alternative | | Path to a file holding the same value (for example a Docker secret). Mutually exclusive with `ENCRYPTION_KEYS` |
| `DATA_DIR` | no | `/data` | SQLite location |
| `LISTEN_ADDR` | no | `:8080` | Listen address |
| `SESSION_MAX_AGE` | no | `720h` | Absolute session cap; positive, at most `2160h` |
| `ACCESS_TOKEN_TTL` | no | `1h` | MCP access token lifetime; between `5m` and `24h` |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | no | `json` | `json` or `text` |
| `TRUSTED_PROXIES` | behind a reverse proxy | | Comma-separated CIDRs of reverse proxies whose `X-Forwarded-For` client address is trusted for rate limiting (see [Deployment](#deployment)) |
| `INSECURE_ALLOW_HTTP` | no | `false` | Allows an `http://` `PUBLIC_URL`. Development and tests only |

`ENCRYPTION_KEYS_FILE` is the only variable with a `_FILE` form. Prefer it
with a Docker secret over putting keys in the environment.

Generate a key and prefix it with an ID:

```sh
echo "k1:$(openssl rand -base64 32)"
```

## Identity-provider setup

Mealie performs the login, so the provider needs no knowledge of this server
except one thing: add `<PUBLIC_URL>/callback` as an allowed redirect URI on
the OIDC client Mealie uses. Keep Mealie's own redirect URIs as they are.

The provider must send `email_verified: true` for the user. Mealie refuses the
login otherwise.

- **Zitadel:** open the application Mealie uses and add the URL under
  Redirect URIs.
- **Authentik:** edit the OAuth2/OpenID provider and add the URL under
  Redirect URIs/Origins.
- **Keycloak:** open the client and add the URL under Valid redirect URIs.
- **Pocket ID:** edit the OIDC client and add the URL to its callback URLs.
- **Google and Entra ID:** these providers restrict redirect URIs per client.
  Mealie v3.29.0 and later accept `OIDC_NATIVE_CLIENT_ID` (with optional
  `OIDC_NATIVE_CONFIDENTIAL`) to give the native endpoints their own client.
  Create a client for it, register the callback URL there, and set the
  variable in Mealie. Without it, Mealie uses `OIDC_CLIENT_ID`.

## Connect

- **Claude.ai:** Settings, Connectors, Add custom connector, and enter
  `<PUBLIC_URL>/mcp`.
- **Claude Code:**

  ```sh
  claude mcp add --transport http mealie <PUBLIC_URL>/mcp
  ```

The first connection opens Mealie's login, then one consent screen.

ChatGPT, Cursor, and VS Code should work but are untested.

## Tools

| Tool | Actions |
|---|---|
| `whoami` | none |
| `account_read` | `household`, `group` (both accept `full`), `households` |
| `recipes_read` | `search`, `get` (accepts `full`), `suggest` |
| `recipes_write` | `create`, `import_url`, `update`, `duplicate`, `delete`, `mark_made`, `set_image_url`, `add_asset_url`, `share`, `bulk_tag`, `bulk_categorize`, `bulk_delete` |
| `history_read` | `timeline` |
| `history_write` | `add`, `made_it`, `update`, `delete` |
| `mealplans_read` | `list`, `today`, `rules` |
| `mealplans_write` | `create`, `bulk_create`, `random`, `update`, `delete`, `rule_create`, `rule_update`, `rule_delete` |
| `organizers_read` | `list`, `get` (accepts `full`), `empty` |
| `organizers_write` | `create`, `update`, `delete`, `merge` |
| `shopping_read` | `lists`, `list` (accepts `full`) |
| `shopping_write` | `create_list`, `update_list`, `delete_list`, `add_items`, `update_items`, `delete_items`, `add_recipe`, `remove_recipe` |
| `ingredients_read` | `list`, `get` (accepts `full`) and `empty` (all need `kind`), `parse` (no `kind`) |
| `ingredients_write` | `create`, `update`, `delete`, `merge` |
| `feedback_read` | `comments`, `ratings`, `favorites` |
| `feedback_write` | `comment`, `update_comment`, `delete_comment`, `rate`, `favorite`, `unfavorite` |
| `mealie_api` | none |

Tools ending in `_read` only read data, so clients can set them to "always
allow". Tools ending in `_write` can change and delete data. `mealie_api` can both
read and write, so don't set it to always allow.

`mealie_api` calls a Mealie API endpoint as the logged-in user, for everyday
areas that no grouped tool covers. It blocks admin, auth and API-token,
user-management (other users), webhook and notification, invitation,
household and group settings, AI-provider, iCal-feed, recipe-action, export,
and media endpoints. It is an allowlist, so endpoints added in future Mealie
releases stay blocked until they are added on purpose.

Tool names, action names, and argument meanings are a public interface.
Breaking changes need a major version or a deprecation period.

## Sessions

- A session ends after `SESSION_MAX_AGE` (default 720h, 30 days).
- The idle limit follows Mealie's `TOKEN_TIME` (48 h by default): the Mealie
  token is renewed on use once it is past half its lifetime, and a session
  that is not used for longer than the token lifetime cannot be renewed.
- A Mealie outage keeps sessions; only a `401` from Mealie ends one.
- Revoke a session by disconnecting the connector in the client or by calling
  `/revoke`. The next call then needs a new login.

Login, consent, token, and session events are logged as JSON lines with
`log_type` set to `audit`.

## Backups

All state lives in `/data/mealie-mcp.db`. Take a consistent copy while the
server runs. The image has no shell and no `sqlite3`, so run the command from
a separate container that mounts the data volume, for example with Alpine
(replace `<project>_mcp-data` with your volume name from `docker volume ls`):

```sh
docker run --rm -v <project>_mcp-data:/data -v "$PWD":/backup alpine:3 \
  sh -c "apk add --no-cache sqlite >/dev/null && sqlite3 /data/mealie-mcp.db \"VACUUM INTO '/backup/mcp.db'\""
```

On a host with `sqlite3` installed and direct access to the volume's
directory, `sqlite3 <volume dir>/mealie-mcp.db "VACUUM INTO 'mcp.db'"` does
the same.

Losing the database only forces everyone to log in again. Keep the encryption
keys separately: the database is useless without them.

## Encryption key rotation

1. Generate a new key and add it first in `ENCRYPTION_KEYS`, for example
   `k2:<new>,k1:<old>`.
2. Restart the server. New data is encrypted with `k2`; `k1` still decrypts
   older data.
3. Wait for `SESSION_MAX_AGE` (30 days by default) so every session created
   under `k1` has ended.
4. Remove `k1` from `ENCRYPTION_KEYS` and restart.

## Development

Requires Go 1.27 or later.

```sh
make build              # build
make test               # tests with race detector, writes coverage.out
make cover              # tests plus per-package coverage minimums
make lint               # golangci-lint, writes golangci-report.xml
make vuln               # govulncheck
make fuzz               # run every fuzz target for FUZZTIME (default 30s)
make test-integration   # Docker-based end-to-end test
gofmt -l .              # list unformatted files
go run ./cmd/mealie-oauth-mcp
```

Enable the pre-commit hook once per clone. It rejects commits that contain
unformatted Go files:

```sh
make setup
```

### Project layout

```
cmd/mealie-oauth-mcp/   main package: config, wiring, startup
internal/               server packages (not importable by other modules)
deploy/                 example Compose file
```


## License

MIT, see [LICENSE](LICENSE).
