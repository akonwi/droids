# Documentation

Project documentation is organized by purpose:

- [`../README.md`](../README.md) — public overview and usage
- [`../VISION.md`](../VISION.md) — design rationale and architectural direction
- [`../AGENTS.md`](../AGENTS.md) — operational guidance and architecture invariants
- [`../BACKLOG.md`](../BACKLOG.md) — outstanding work
- [`adrs/`](./adrs/) — durable records of consequential architecture decisions

## Architecture Decision Records

Use an Architecture Decision Record (ADR) when a decision materially affects the public API, provider boundaries, persistence model, runtime behavior, or another architectural constraint that future contributors need to understand.

ADRs are numbered sequentially and use lowercase kebab-case filenames:

```text
adrs/0002-example-decision.md
```

Each ADR uses this structure:

```markdown
# NNNN: Title

## Status

Proposed

## Context

## Decision

## Consequences

## Related
```

Use `Proposed` while a decision is under discussion and `Accepted` once adopted. Do not rewrite an accepted decision to reflect a later direction; add a new ADR that supersedes it and link the two records.
