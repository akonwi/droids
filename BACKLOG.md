# Backlog

Deferred work. The seams exist today; these fill them in. See `VISION.md` for
the architecture.

Delete items once they are done — this file tracks only outstanding work.

## Providers
- [ ] OAuth / credential-store flows (api-key + custom headers only today —
      fits AI Gateway).

## Loop
- [ ] Propagate caller context cancellation into the active run. Today `ctx`
      only stops the `Run`/`Result` wait; the worker keeps executing on an
      internal background context and `Close()` does not abort an in-flight
      turn. A cancelled or timed-out run can therefore still complete provider
      calls and tool side effects. Fix: thread the caller `ctx` into the running
      turn and cancel provider/tool execution on `ctx.Done()` (and/or have
      `Abort()`/`Close()` stop the active run).
