# Backlog

Deferred work. The seams exist today; these fill them in. See `VISION.md` for
the architecture.

Delete items once they are done — this file tracks only outstanding work.

## Providers
- [ ] OAuth / credential-store flows (api-key + custom headers only today —
      fits AI Gateway).

## Loop
- [ ] `Events()` after `Close()` returns a fresh channel that never closes,
      violating the "same channel every call" contract. `finishEvents` nils the
      channel; keep the closed channel (or return a pre-closed one) so post-close
      callers get a closed channel.
