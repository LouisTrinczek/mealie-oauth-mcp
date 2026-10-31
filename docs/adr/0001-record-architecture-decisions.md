# 1. Record architecture decisions

Status: Accepted

Date: 2026-10-31

## Context

The project is built to be run by self-hosters and changed by contributors.
Both groups need to know why the code is shaped the way it is. Reasons that
live only in someone's head, or in a closed pull request, get lost.

## Decision

We record each significant architecture decision as a short file in
`docs/adr/`, named `NNNN-short-title.md` with the next free number. Each file
has a title, `Status`, `Date`, and three sections: Context, Decision and
Consequences. It stays under about 40 lines.

A decision is never rewritten after the fact. To change one, add a new record
and set the old record's status to `Superseded by NNNN`.

## Consequences

- New contributors can read the reasons before proposing a change.
- Reversing a decision is a visible, reviewed act.
- Writing a record costs a few minutes per decision. Small choices that the
  code explains on its own do not need one.
