# Decision records

Each record explains one architecture decision: the context, what we decided,
and what follows from it. For the big picture, start with the
[architecture overview](../architecture.md).

| # | Decision |
|---|---|
| [1](0001-record-architecture-decisions.md) | Record architecture decisions in this format |
| [2](0002-go-and-official-mcp-sdk.md) | Write the server in Go with the official MCP Go SDK |
| [3](0003-delegate-login-to-mealie-oidc.md) | Delegate login to Mealie's native OIDC flow |
| [4](0004-opaque-hashed-tokens-and-aes-gcm.md) | Opaque, hashed MCP tokens instead of JWTs; Mealie tokens encrypted with AES-GCM and rotatable keys |
| [5](0005-single-instance-with-sqlite.md) | Run a single instance with SQLite |
| [6](0006-grouped-tools-with-action-argument.md) | Group tools by area into read and write tools with an `action` argument |
| [7](0007-mealie-api-fail-closed-allowlist.md) | Make the `mealie_api` escape hatch a fail-closed allowlist |
| [8](0008-handwritten-lenient-mealie-client.md) | Use a hand-written, lenient Mealie client with field-preserving updates |
| [9](0009-consent-once-per-user-and-client.md) | Show a consent screen once per user and client |
| [10](0010-admins-may-log-in.md) | Let admins log in; keep admin features unreachable |
| [11](0011-observability-and-health-endpoints.md) | Metrics on a separate listener, optional OTLP tracing, separate liveness and readiness |
| [12](0012-releases-and-supply-chain.md) | Release with release-please and GoReleaser, build the image from the Dockerfile, sign keylessly |
| [13](0013-opentelemetry-metrics-with-prometheus-exporter.md) | Instrument metrics with OpenTelemetry and export them for Prometheus |

## Adding a record

1. Take the next free number and pick a short title.
2. Copy [0001](0001-record-architecture-decisions.md) as the format: title,
   `Status`, `Date`, then Context, Decision and Consequences. Keep it under
   about 40 lines.
3. Add a row to the table above.
4. To change an earlier decision, write a new record and set the old one's
   status to `Superseded by NNNN`. Do not rewrite history.
