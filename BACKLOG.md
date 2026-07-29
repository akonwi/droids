# Backlog

Deferred work. The seams exist today; these fill them in. See `VISION.md` for
the architecture.

Delete items once they are done — this file tracks only outstanding work.

## Providers
- [ ] Additional providers (Anthropic, Google, …) — `Provider` is an interface,
      so they slot in without core changes.
- [ ] OAuth / credential-store flows (api-key + custom headers only today —
      fits AI Gateway).

## Loop
- [ ] Parallel tool execution (loop runs tools sequentially today). Revisit
      the ordering contract for `before`/`after` hooks when this lands.

## Durable runtime (belongs in the runtime that wraps `droids`)
- [ ] Idempotency for tool side effects.
- [ ] Per-user concurrency control.
- [ ] Leasing / recovery across process restarts.
