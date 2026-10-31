# 3. Delegate login to Mealie's native OIDC flow

Status: Accepted

Date: 2026-10-31

## Context

Users already log in to Mealie through an identity provider. An MCP server
needs its own login for each agent, and the agent must act as that Mealie
user. Alternatives are a static Mealie API token behind an open URL, pasted
tokens, or a second OIDC client registered at the identity provider.

Mealie offers a native flow: `GET /api/auth/oauth/native/config` returns its
OIDC settings, and `POST /api/auth/oauth/native/token` exchanges an
authorization code for a normal Mealie session token. Mealie holds the client
secret and validates the ID token.

## Decision

The server acts as an OAuth authorization server toward MCP clients and as an
OIDC relay toward Mealie. It sends the browser to the identity provider with
Mealie's client ID, receives the code on `/callback`, and hands it to Mealie's
native token endpoint. It then reads `GET /api/users/self` to learn who logged
in. Any identity provider that works with Mealie works here.

## Consequences

- No client secret and no identity-provider tokens in this server.
- Setup needs one step: the identity provider must allow
  `PUBLIC_URL/callback` on Mealie's OIDC client, because Mealie does not check
  the redirect URI.
- Mealie never asks the identity provider again after login. The absolute
  `SESSION_MAX_AGE` cap is the only way a deactivated account loses access.
- The flow depends on Mealie's native endpoints. The client decodes responses
  leniently, and `make dev-up` runs the flow against a local Mealie.
