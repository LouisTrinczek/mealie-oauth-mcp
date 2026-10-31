# 2. Go with the official MCP Go SDK

Status: Accepted

Date: 2026-10-31

## Context

The server must run as a small, static container for self-hosters, on amd64
and arm64, and speak the MCP Streamable HTTP transport with OAuth
bearer-token protection. Several languages and SDKs could do this.

## Decision

We write the server in Go and use the official MCP Go SDK
(`github.com/modelcontextprotocol/go-sdk`) for the protocol and for its
bearer-token middleware. Everything else uses the standard library first:
`net/http` for routing, `log/slog` for logging, `crypto/*` for cryptography.
The binary builds with `CGO_ENABLED=0`, so the image can be static and
multi-arch. New dependencies need agreement before they are added.

## Consequences

- One static binary and a tiny image with no shell and no runtime.
- Protocol changes arrive through SDK upgrades instead of our own code.
- We depend on the SDK's release pace and API stability. Dependabot keeps it
  current, and the surface tests catch changes to the tool listing.
- Pure-Go libraries only. This is why the SQLite driver is pure Go too (see
  [ADR 5](0005-single-instance-with-sqlite.md)).
