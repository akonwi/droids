# 0001: Record architecture decisions

## Status

Accepted

## Context

The project has design rationale, operational invariants, and implementation guidance spread across its existing documentation and source code. Consequential decisions need a concise, durable record that explains why a direction was chosen, its tradeoffs, and its relationship to later decisions.

## Decision

Record consequential architecture decisions as Markdown files in `docs/adrs/`.

ADRs are numbered sequentially, use lowercase kebab-case filenames, and contain Status, Context, Decision, Consequences, and Related sections. New records begin as `Proposed` unless the decision has already been accepted.

Accepted ADRs remain historical records. A later change is documented in a new ADR that supersedes the earlier record rather than rewriting the original decision.

## Consequences

- Contributors have a consistent place to discover the reasoning behind architectural constraints.
- Proposed decisions can be reviewed before implementation.
- Maintaining ADRs adds a small documentation cost when consequential decisions are made.
- Existing documents keep their current roles and may link to ADRs where appropriate.

## Related

- [`../README.md`](../README.md)
- [`../../VISION.md`](../../VISION.md)
- [`../../AGENTS.md`](../../AGENTS.md)
