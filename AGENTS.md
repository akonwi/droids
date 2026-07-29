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
| `provider.go` | `Provider` interface + `NewProvider` registry (routes by model). |
| `provider_openai.go` | OpenAI-compatible provider, backed by `openai-go`. |
| `tool.go` | Generic `Tool[Args]` + `NewTool` erasing to `AnyTool`. |
| `storage.go` | `Storage` seam + `MemoryStorage` (the default). |
| `event.go` | Agent-level `Event` union emitted by `Droid.Events()`. |
| `droid.go` | The `Droid` facade: `New`, `Run`, `Send`, `Steer`, `Abort`, `Events`, `Close`. |
| `loop.go` | The bounded tool loop that drives turns. |
| `cmd/chat` | A CLI REPL example for manual testing. |
| `*_test.go` | Tests. `example_live_test.go` is build-tagged `live`. |

## Architecture invariants — do not break these

1. **Two distinct event protocols, kept separate.**
   - `StreamEvent` (`stream.go`) is the *provider* layer: `StreamStart` → deltas
     → exactly one `StreamDone` **or** `StreamError`. Providers must encode
     failures as `StreamError` (with `StopReason` error/aborted), never as a
     returned Go error.
   - `Event` (`event.go`) is the *agent* layer emitted by `Droid.Events()`. The
     loop consumes `StreamEvent`s and re-emits higher-level `Event`s.
2. **Sealed interfaces.** `Message`, `Content`, `StreamEvent`, and `Event` are
   sealed via unexported marker methods (`isMessage()`, `isContent()`, etc.).
   Add a variant by defining the struct and the marker method. Consumers
   type-switch on concrete types.
3. **`Events()` is one long-lived channel per droid**, created lazily. If no one
   calls `Events()`, the loop still runs and still persists. Never assume a
   consumer exists; `emit` drops when there is none.
4. **Persistence is independent of the event channel.** The loop appends to
   `Storage` on every completed message regardless of whether events are being
   consumed. `Storage` defaults to `MemoryStorage`; it is never nil after `New`.
5. **Providers translate only at the edge.** The loop, storage, and events see
   only neutral types (`message.go`). Wire/SDK types stay inside `provider_*.go`.
6. **Layers stay visible.** `Droid` composes `Provider` + loop + `Storage`; it
   must not hide them. `Provider` is usable standalone for raw streaming.
7. **`context.Context` threads through** all blocking calls. `Abort()` is
   ergonomic sugar over cancelling the active run's context.

## Adding a provider

1. Add `provider_<name>.go` with a config struct implementing `ProviderConfig`
   (`build() (providerEntry, error)`).
2. Translate `Request` → wire format and the wire stream → `StreamEvent`s. Use a
   `pipeStream` (see `provider_openai.go`) as the `Stream` implementation.
3. Encode all failures as `StreamError`; set `StopReasonAborted` when `ctx` is
   done.
4. `Provider` is an interface — no changes needed to the registry.

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

- JSON-Schema derivation from `Tool[Args]` (currently hand-written `Parameters`).
- Parallel tool execution + `before`/`after` tool hooks (loop is sequential).
- OAuth / credential-store flows (api-key + headers only today).
- Additional providers (Anthropic, Google, …).
- A Postgres `Storage` for durable server runs; idempotency, per-user
  concurrency, and leasing belong in the runtime that wraps `droids`.
