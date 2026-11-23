# 14. Support opt-in Mealie password sign-in over verified TLS

Status: Accepted

Date: 2026-11-23

## Context

Mealie instances without OIDC need a way for users to authorize the MCP with
their own accounts. Mealie's password login offers no browser authorization
code handoff. Supersedes [ADR 0003](0003-delegate-login-to-mealie-oidc.md).

## Decision

Keep native OIDC when enabled in Mealie. Offer a Mealie password form only
when Mealie allows password login and the MCP operator opts in. Both methods
claim the same short-lived, browser-bound pending authorization. Password
submission requires CSRF validation, rate limiting and inbound TLS. The MCP
sends credentials to the configured Mealie server over verified HTTPS with
redirects disabled. Passwords stay out of URLs, logs, traces and storage.

## Consequences

Both login methods reuse consent and encrypted Mealie token storage. The MCP
and each TLS-terminating proxy handle passwords during sign-in. Proxies use
HTTPS on every hop that carries a password and verify upstream certificates.
OIDC-only deployments can retain their existing proxy configuration.
