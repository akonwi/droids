# droids

A reusable Go agent library. One import gives you a multiprovider LLM
abstraction, a bounded tool-calling loop, and a durable-persistence seam —
composed behind a small `Droid` facade, but usable layer by layer.

Its lineage: the design distills [pi](https://github.com/earendil-works/pi)'s
`pi-ai` (multiprovider streaming) and `pi-agent-core` (the agent loop) into
idiomatic Go, and folds in the durability/orchestration requirements from the
Go Agent Runtime design (durable runs, retry/resume, observability,
cancellation). It is **not** a high-fidelity pi port: it starts with the
OpenAI-compatible provider and grows outward.

## Three layers, visible and independently usable

```
┌─────────────────────────────────────────────────────────────┐
│ Storage           durable persistence + observability seam   │  Layer 3
│                   (Load / Append; defaults to in-memory)     │
├─────────────────────────────────────────────────────────────┤
│ Droid / loop      bounded multi-step tool loop, steering,    │  Layer 2
│                   follow-up, abort, event stream             │
├─────────────────────────────────────────────────────────────┤
│ Providers         model abstraction + multiprovider          │  Layer 1
│                   streaming (registry over Provider configs)  │
└─────────────────────────────────────────────────────────────┘
```

The `Droid` facade composes all three, but `Providers` (Layer 1) is usable on
its own for raw streaming, and `Storage` is a plain interface you back with
Postgres or leave nil.

## Shape of the API

```go
providers, _ := droids.NewProviders(
    droids.OpenAI{APIKey: key, BaseURL: gateway}, // AI Gateway = custom BaseURL
    // droids.Anthropic{...},                      // later
)

d, _ := droids.New(droids.Options{
    Providers:    providers,
    Model:        "gpt-5.6-sol",
    SystemPrompt: "...",
    Tools:        []droids.AnyTool{echoTool},
    Storage:      pgStore,   // durable runs + resume; nil => in-memory
    Session:      "run-42",  // rehydrates transcript on New
    MaxSteps:     8,         // bounded loop
})

// one-shot (e.g. Inbox Triage): blocks, returns the final message
msg, err := d.Run(ctx, "triage this email ...")

// streaming (e.g. conversational agent): long-lived event channel
d.Send(ctx, "message ...")      // starts a turn, or a follow-up if busy
d.Steer("actually focus on X")  // inject mid-run, picked up between turns
d.Abort()                        // interrupt the active run
for ev := range d.Events() {
    switch e := ev.(type) {
    case droids.MessageDelta:       // streaming assistant output
    case droids.ToolExecutionEnd:   // a tool finished
    case droids.TurnEnd:            // one assistant turn + tool results
    case droids.ErrorEvent:         // failed/aborted
    }
}
```

## Settled design decisions

- **Events are a sealed interface** (`Event` with `isEvent()`), type-switched
  by consumers. Chosen over a tagged struct for compile-checked variants and
  self-contained payloads.
- **`Events()` is one long-lived channel** per droid, created lazily. If you
  never call it, the loop still runs and still persists — live events are
  purely for UI. `Run` observes completion via a private result channel, so
  one-shot callers need not drain events.
- **Tools are generic** (`Tool[Args]`) with a typed `Execute`, erased into
  `AnyTool` via `NewTool`. Arguments are JSON-decoded into `Args` before the
  call, and the tool's JSON Schema is derived from `Args` by reflection (set
  `Parameters` explicitly to override).
- **Providers are a registry.** `NewProviders(...Provider)` composes provider
  configs into one routing `Providers` that dispatches by the model's owning
  provider. Model ids resolve bare, or namespaced (`"provider/model"`) to
  disambiguate. Mirrors pi's `Models` layer.
- **Storage is `Load` + `Append`.** The loop appends on every completed
  message, independent of the events channel, so persistence works whether or
  not anyone is watching. Defaults to an in-memory store; back it with Postgres
  for durable runs.
- **Layers stay visible.** The facade composes; it does not hide `Providers`,
  the loop, or `Storage`.

## Neutral vocabulary

Messages (`UserMessage`, `AssistantMessage`, `ToolResultMessage`) and content
blocks (`TextContent`, `ThinkingContent`, `ImageContent`, `ToolCall`) are
provider-neutral. Providers translate to/from their wire format at the edge;
the loop and storage only ever see these types. `Providers.Stream` emits a
`StreamEvent` protocol (start / deltas / done / error) that assembles into one
`AssistantMessage`; the loop re-emits higher-level agent `Event`s.
