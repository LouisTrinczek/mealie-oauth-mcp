# 6. Grouped read and write tools with an `action` argument

Status: Accepted

Date: 2026-10-31

## Context

Mealie has hundreds of endpoints. One tool per endpoint floods the agent's
context with tool definitions and makes it pick between near-duplicates. One
giant tool hides what is safe. Clients also let users approve tools by name,
for example "always allow" for read-only ones.

## Decision

Tools are grouped by area, such as recipes, meal plans and shopping. Each area
has a `_read` tool and a `_write` tool. The tool takes an `action` argument
that selects the operation. Read tools only read, so clients can allow them
without prompting. Write tools can change and delete data. Output is compact:
lists show a few useful fields, and `get` results drop nulls and empty values.

Tool names, action names and argument meanings are a public interface. The
[tool stability policy](../../CONTRIBUTING.md#tool-stability-policy) governs
changes, and a golden test over the listed tool surface catches accidents.

## Consequences

- A small tool list keeps the context cost low for agents.
- The read/write split lets users set trust per tool.
- Adding an operation means adding an action, not a tool. See
  [Adding a tool](../../CONTRIBUTING.md#adding-a-tool).
- Action schemas are a little more complex than single-purpose tools.
