# 10. Admins may log in; admin features stay unreachable

Status: Accepted

Date: 2026-10-31

## Context

Some Mealie users are administrators. We could refuse their login, or let them
in and keep admin powers out of the agent's reach. Refusing the login would
lock out the people most likely to set the server up, and it would not remove
the risk for non-admin users who share an instance.

## Decision

Admin users can log in like anyone else. The login flow does not look at the
admin flag to decide access. Instead, admin features are unreachable through
the tool surface. No grouped tool calls admin endpoints, and the `mealie_api`
allowlist (see [ADR 7](0007-mealie-api-fail-closed-allowlist.md)) excludes
them.

The `whoami` tool reports whether the user is an admin, so an agent can
explain what it cannot do.

## Consequences

- Admins use the same connection as everyone else and get the same limits.
- The protection lives in one place, the tool surface, and has tests.
- An admin who wants to run admin tasks through an agent cannot. They use
  Mealie directly.
- A bug in the allowlist is more serious for admin sessions, which is one more
  reason it fails closed.
