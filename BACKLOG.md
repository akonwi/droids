# Backlog

Deferred work. The seams exist today; these fill them in. See `VISION.md` for
the architecture.

## Providers
- [ ] Additional providers (Anthropic, Google, …) — `Provider` is an interface,
      so they slot in without core changes.
- [ ] OAuth / credential-store flows (api-key + custom headers only today —
      fits AI Gateway).

## Loop
- [ ] Parallel tool execution (loop runs tools sequentially today).
- [ ] `before` / `after` tool hooks.

## Tools
- [ ] JSON-Schema derivation from `Tool[Args]` (currently hand-written
      `Parameters`).

## Durable runtime (belongs in the runtime that wraps `droids`)
- [ ] Idempotency for tool side effects.
- [ ] Per-user concurrency control.
- [ ] Leasing / recovery across process restarts.
