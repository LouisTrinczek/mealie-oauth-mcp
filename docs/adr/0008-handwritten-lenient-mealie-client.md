# 8. A hand-written, lenient Mealie client with field-preserving updates

Status: Accepted

Date: 2026-10-31

## Context

Mealie ships every one to two weeks and adds fields freely. A client generated
from Mealie's OpenAPI document would be large, would track every release, and
would carry types for hundreds of endpoints we never call. A typed struct that
is read and written back drops fields it does not know, which silently damages
user data.

## Decision

The Mealie client is hand-written and covers only what the server uses.
Responses are decoded leniently: unknown fields never cause errors.

We never update a Mealie object by sending a typed struct back. We use `PATCH`
with only the changed fields. Where Mealie only offers `PUT`, we `GET` the raw
JSON, merge the changes into it, and `PUT` it back, so unknown fields survive.

## Consequences

- The client stays small and readable, and a Mealie release rarely breaks it.
- Each new call needs hand-written code and tests.
- Updates are safe against fields added by newer Mealie versions.
- Release notes say which Mealie version a release was tested against.
