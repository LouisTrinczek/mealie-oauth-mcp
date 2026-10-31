# 4. Opaque, hashed MCP tokens; AES-GCM for Mealie tokens

Status: Accepted

Date: 2026-10-31

## Context

The server holds two kinds of secrets. MCP tokens go to agents. Mealie session
tokens stay on the server and are used to call Mealie as the user. A leak of
the database or a log must not give anyone access, and a user must be able to
cut off an agent immediately.

## Decision

MCP access, refresh and authorization-code values are random opaque strings.
Only their SHA-256 hashes are stored. They are not JWTs, so there is nothing
to verify offline and revocation takes effect on the next request.

Mealie session tokens are stored encrypted with AES-GCM. `ENCRYPTION_KEYS` is a
list of `id:key` pairs: the first key encrypts and all keys decrypt, and the
key ID is stored with the data. Context such as the session ID is bound in as
additional authenticated data.

Refresh tokens rotate on every use. Reuse of a rotated token or of an
authorization code revokes the session.

## Consequences

- A stolen database yields no usable MCP tokens and no readable Mealie tokens
  without the keys.
- Every request does one database lookup. This is acceptable for a single
  instance.
- Keys rotate without downtime: add a new first key, wait for
  `SESSION_MAX_AGE`, then drop the old one.
- Losing all keys makes stored sessions unreadable. Users log in again.
