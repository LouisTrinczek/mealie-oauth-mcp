# 9. A consent screen once per user and client

Status: Accepted

Date: 2026-10-31

## Context

Clients can register themselves, so anyone can register a client with their
own redirect URI and send a victim an `/authorize` link. If the victim is
already signed in to the identity provider, login would finish silently and
hand the attacker a working session. This is the confused-deputy problem
described in the MCP security guidance.

## Decision

After the identity-provider callback, the server shows a minimal consent page
with the client name, the host of its redirect URI and the Mealie user name,
with Allow and Deny. The page is protected by a CSRF token and bound to the
browser that started the login with a cookie. Approval is remembered for each
pair of Mealie user and client ID, so a user sees it once per client. The page
sets a content security policy that limits form targets to the redirect URI.

## Consequences

- A silent login by an attacker's client is not possible without the user
  clicking Allow.
- Users see one extra click the first time they connect a client.
- Consent rows are stored and cleaned up with the rest of the state.
- The page tells the user what they approve, so client names and redirect
  hosts must be shown honestly.
