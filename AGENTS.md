# AGENTS.md — droids

Guidance for agents (and humans) working on this repository. Read this fully
before making changes. See `VISION.md` for the design rationale and decision
log; this file is the operational reference.

## What this is

`droids` is a standalone Go library for building agents. One import provides
three composable, independently usable layers:

```
Storage      durable persistence + observability seam (Layer 3)
Droid/loop   bounded multi-step tool loop, streaming, steering (Layer 2)
Provider     model abstraction + multiprovider streaming (Layer 1)
```

Lineage: it distills [pi](https://github.com/earendil-works/pi)'s `pi-ai`
(multiprovider streaming) and `pi-agent-core` (the agent loop) into idiomatic
Go, plus the durability requirements from a server-side agent runtime. It is
**not** a high-fidelity pi port — it starts with the OpenAI-compatible provider
and grows outward.

## Layout

| File | Responsibility |
|------|----------------|
| `message.go` | Neutral conversation vocabulary: `Message`, content blocks. |
| `model.go` | `Model` metadata, `Usage`/cost accounting. |
| `stream.go` | Provider streaming contract: `Request`, `StreamEvent`, `Stream`. |
| `provider.go` | `Providers` registry + `NewProviders` (routes by model); `Provider` config interface. |
| `provider_openai.go` | OpenAI-compatible provider, backed by `openai-go`. |
| `provider_anthropic.go` | Anthropic Messages provider, backed by `anthropic-sdk-go`. |
| `provider_cloudflare.go` | Cloudflare AI Gateway decorator over OpenAI/Anthropic configs. |
| `tool.go` | Generic `Tool[Args]` + `NewTool` erasing to `AnyTool`. |
| `mcp/` | Optional progressive-disclosure adapter for MCP tool namespaces. |
| `storage.go` | `Storage` seam + `MemoryStorage` (the default). |
| `event.go` | Agent-level `Event` union emitted by session and per-run streams. |
| `droid.go` | The `Droid` facade: `New`, `Execute`, `Stream`, `Continue`, `ContinueStream`, `Send`, `Steer`, `Abort`, `Events`, `Close`. |
| `run.go` | Per-run event stream and reusable final result. |
| `loop.go` | The bounded tool loop that drives turns. |
| `cmd/chat` | A CLI REPL example for manual testing. |
| `*_test.go` | Tests. `example_live_test.go` is build-tagged `live`. |

## Architecture invariants — do not break these

1. **Two distinct event protocols, kept separate.**
   - `StreamEvent` (`stream.go`) is the *provider* layer: `StreamStart` → deltas
     → exactly one `StreamDone` **or** `StreamError`. Providers must encode
     failures as `StreamError` (with `StopReason` error/aborted), never as a
     returned Go error.
   - `Event` (`event.go`) is the *agent* layer emitted by `Droid.Events()` or
     `Run.Events()`. The loop consumes `StreamEvent`s and re-emits
     higher-level `Event`s.
2. **Sealed interfaces.** `Message`, `Content`, `StreamEvent`, and `Event` are
   sealed via unexported marker methods (`isMessage()`, `isContent()`, etc.).
   Add a variant by defining the struct and the marker method. Consumers
   type-switch on concrete types.
3. **Two agent event scopes.** `Droid.Events()` is one long-lived channel per
   droid, created lazily and closed only at shutdown. `Run.Events()` is scoped
   to one run and closes when that run ends; those events are delivered
   exclusively to the per-run stream. If neither is requested, the loop still
   runs and persists.
4. **Persistence is independent of the event channel.** The loop appends to
   `Storage` on every completed message regardless of whether events are being
   consumed. `Storage` defaults to `MemoryStorage`; it is never nil after `New`.
5. **Providers translate only at the edge.** The loop, storage, and events see
   only neutral types (`message.go`). Wire/SDK types stay inside `provider_*.go`.
6. **Layers stay visible.** `Droid` composes `Providers` + loop + `Storage`; it
   must not hide them. `Providers` is usable standalone for raw streaming.
7. **`context.Context` threads through** all blocking calls. `Abort()` is
   ergonomic sugar over cancelling the active run's context.

## Adding a provider

1. Add `provider_<name>.go` with a config struct implementing `Provider`
   (`build() (providerEntry, error)`).
2. Translate `Request` → wire format and the wire stream → `StreamEvent`s. Use a
   `pipeStream` (see `provider_openai.go`) as the `Stream` implementation.
3. Encode all failures as `StreamError`; set `StopReasonAborted` when `ctx` is
   done.
4. `Providers` is an interface — no changes needed to the registry.

## Validation (run before every commit)

```sh
gofmt -l .        # must print nothing
go build ./...
go vet ./...
go test ./...
```

The live smoke test is opt-in and hits a real endpoint:

```sh
OPENAI_API_KEY=sk-... go test -tags live -run TestLive -v ./...
```

## Conventions

- Standard Go style; keep exported identifiers documented.
- Commit with Conventional Commits — see `.agents/skills/commit/SKILL.md`.
- Prefer surgical edits. Keep the neutral core provider-agnostic.

## Deferred work (define the seam now, implement later)

Tracked as a checklist in `BACKLOG.md`.
