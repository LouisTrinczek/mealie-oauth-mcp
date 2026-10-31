# 7. The `mealie_api` escape hatch as a fail-closed allowlist

Status: Accepted

Date: 2026-10-31

## Context

The grouped tools cannot cover every everyday Mealie endpoint. Agents need a
way to reach the rest. Mealie also has endpoints that must never be reachable
through an agent: admin, authentication, API tokens, user management,
webhooks and notifications. Mealie ships every one to two weeks and adds new
endpoints freely.

## Decision

`mealie_api` calls a Mealie endpoint as the logged-in user, but only if the
method and path match an allowlist. A blocklist would let every new Mealie
endpoint through by default. The allowlist denies by default, so new endpoints
stay blocked until someone adds them on purpose.

Paths are normalised before matching. Only `/api/` paths are accepted. Query
strings and fragments, backslashes, double encoding, and empty or dot segments
are rejected. Every rule has tests, including bypass attempts.

## Consequences

- A new Mealie endpoint is unreachable through `mealie_api` until the
  allowlist is extended in a reviewed change.
- Users sometimes hit a denial for an endpoint they think is harmless. The
  error says what is allowed.
- The allowlist needs upkeep as Mealie grows.
- The tool can read and write, so clients should not set it to "always allow".
