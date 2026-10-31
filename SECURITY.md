# Security policy

## Supported versions

Only the latest minor release gets security fixes.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's private vulnerability
reporting. Open the Security tab and choose "Report a vulnerability", or go to
<https://github.com/LouisTrinczek/mealie-oauth-mcp/security/advisories/new>.

Never report a vulnerability in a public issue, pull request, or discussion.

Include:

- The affected version or image tag.
- A description of the issue and its impact.
- Steps to reproduce it, or a proof of concept.
- Your Mealie version and identity provider, if they matter.

Remove tokens, auth codes, and secrets from anything you attach.

Expect a first reply within 7 days.

## Scope

These areas matter most:

- Token handling: how MCP tokens and Mealie tokens are issued, stored, and
  logged.
- The OAuth flow: login, PKCE, redirect URIs, and token exchange.
- The `mealie_api` allowlist: any path that gets past it, including through
  path normalization.
- Mealie admin reachability: any tool or path that reaches Mealie admin, auth,
  API-token, user-management, webhook, or notification endpoints.
