# 0003: Add an optional application-managed compaction hook

## Status

Proposed

## Context

A Droids session accumulates neutral messages as its agent loop runs. Long-lived sessions eventually approach the active model's context-window limit.

Consumers can currently inspect persisted messages and provider usage, but each consumer would have to:

- resolve model context limits
- estimate the complete rendered request
- account for system prompts and tool definitions
- reserve output and reasoning capacity
- recognize provider context-overflow errors
- decide when to invoke its own compaction logic
- retry the request safely

That duplicates provider-sensitive logic in every application.

Droids already owns:

- provider routing
- resolved `Model` metadata
- the neutral transcript
- system prompts and tool definitions
- request rendering
- provider usage normalization
- the agent loop

It is therefore the correct layer to detect context pressure.

Droids should not own application-specific summary formats, persistence, checkpoint boundaries, retention, or the model used to perform compaction. A CLI may compact into memory, while a server may run a separate in-memory Droid with a fast summarization model, persist checkpoints in PostgreSQL, and retain its complete transcript.

OpenAI and Anthropic also expose provider-native context-management features, but their artifacts and semantics differ. OpenAI's compaction item is opaque, while Anthropic can produce a readable summary. Encoding either behavior into the neutral loop would make persistence and continuation provider-specific.

## Decision

Add provider-neutral context-pressure detection and an optional, application-managed compaction hook to `Droid`.

The hook is disabled when omitted. Existing consumers retain their current behavior without configuration changes.

### Public API

Add context-budget and compaction types equivalent to:

```go
type ContextUsage struct {
    Model           Model
    EstimatedInput  int
    ReservedOutput  int
    ContextWindow   int
    MaxInputTokens  int
    Remaining       int
    Exact           bool
}

type CompactionReason string

const (
    CompactionThreshold CompactionReason = "threshold"
    CompactionOverflow  CompactionReason = "context_overflow"
)

type CompactionRequest struct {
    Reason   CompactionReason
    Model    Model
    Usage    ContextUsage
    Messages []Message
}

type CompactionResult struct {
    Applied  bool
    Messages []Message
}

type CompactionHook func(
    context.Context,
    CompactionRequest,
) (CompactionResult, error)
```

Add the optional hook to `Options`:

```go
type Options struct {
    // Existing fields...

    Compact CompactionHook
}
```

Droids uses one internal policy: invoke the hook when estimated input plus the actual per-request output allowance reaches 80% of the resolved context window, or when estimated input reaches 80% of a stricter input-only limit. The trigger is deliberately not configurable in the initial API. Consumers neither perform the usage comparison nor need to choose a replacement target.

### Detection

Before the first provider request in a run, Droids estimates the complete rendered input, including:

- system prompt
- messages
- tool definitions
- attachment representations where estimable
- provider-required framing overhead

It compares the estimated input and reserved output capacity with the resolved model context window.

The model registry is authoritative for the catalog capabilities `ContextWindow`, `MaxInputTokens`, and `MaxOutputTokens`. `Options.MaxTokens` is the actual per-request output allowance and is capped by the selected model's output capability. Built-in and refreshed catalogs provide this metadata rather than requiring consumers to configure it.

Droids may improve estimation internally without changing the hook contract. Possible implementations include:

- provider token-counting APIs
- provider-specific local tokenizers
- normalized byte-based estimates
- calibration using actual usage from prior responses

`Exact` communicates whether the estimate came from authoritative token counting.

### Invocation

When context pressure crosses Droids' threshold and `Compact` is non-nil, Droids invokes the hook before calling the provider.

The hook returns the canonical replacement message context for the current Droid. Droids validates that the replacement:

- has a valid neutral-message sequence
- contains no incomplete tool-call/result batch
- is smaller than the original context
- falls below the same fixed pressure trigger that requested compaction, when model limits are known

When `Applied` is false, Droids proceeds without replacing the transcript. The provider remains authoritative and may still reject an oversized request.

The hook may generate a summary using another in-memory Droid and model, persist a checkpoint, write a file, update external storage, or perform no persistence. Droids does not prescribe the compaction model or storage behavior. An in-memory Droid created by a hook must omit its own compaction hook to avoid recursive compaction.

### Transcript and storage behavior

Applying compaction replaces only the Droid's active in-memory context. It does not delete or rewrite messages through the `Storage` interface.

The application is responsible for making reconstruction consistent with its compaction model. For example, a durable application may persist a checkpoint and have its next `Storage.Load` return:

```text
latest checkpoint
messages after checkpoint
```

Subsequent completed messages continue to be appended through the existing `Storage.Append` contract.

This preserves the invariant that Droids never silently deletes a consumer's durable transcript.

### Invocation boundaries

The initial implementation invokes durable compaction only before the first provider request of a run, after the new user input and any pending steering have been appended.

This is a safe consumer boundary: the preceding run is complete, and the new user input and steering can remain in the raw tail.

Compaction between tool-loop steps is deferred. Those steps can contain active tool-call state that an application has not yet represented as a durable thread checkpoint.

### Provider overflow

Providers normalize an input context-window overflow into a neutral Droids condition.

If the first provider request fails because the context is too large and a hook is configured, Droids invokes it with `CompactionOverflow` and retries the provider request once.

Droids never retries a context-overflow compaction indefinitely. A hook result that does not reduce context cannot trigger another compaction cycle for the same provider step.

Provider output-length exhaustion remains distinct from input context overflow and does not trigger compaction.

### Error behavior

If the hook returns an error, Droids returns a run error unless the consumer itself returns a valid fallback context.

A consumer that prefers graceful degradation may catch its own summary or persistence failure and return a bounded recent-message context.

When no hook is configured, context-pressure detection has no behavioral effect. The provider handles the request as it does today.

### Events and observability

Expose compaction through agent-level events so applications can measure it without wrapping the hook:

```go
type CompactionStart struct {
    Reason CompactionReason
    Usage  ContextUsage
}

type CompactionEnd struct {
    Reason  CompactionReason
    Before  ContextUsage
    After   ContextUsage
    Applied bool
}
```

Hook failures continue through the normal `ErrorEvent` path.

No summary content is emitted in observability events by default.

## Consequences

Positive:

- applications do not duplicate context-pressure checks
- context budgeting follows the resolved provider and model
- compaction remains optional
- summary generation, model selection, and persistence remain application-owned
- consumers may use fast models tailored to summarization without changing the active conversation model
- the neutral loop remains independent of provider-native compaction formats
- context overflow can recover through one safe retry
- later estimator improvements do not change consumer hook APIs
- durable storage is never silently truncated

Tradeoffs:

- request token estimation is approximate and may over- or underestimate provider tokenization
- model metadata must include a context window for proactive detection
- hook execution adds latency at the start of affected runs
- applications must reconcile active compacted context with durable storage
- a hook that creates another Droid must avoid recursive compaction
- replacing the active transcript affects subsequent session events and snapshots
- provider overflow errors require additional normalization
- mid-tool-loop context growth remains unresolved initially

## Non-goals

- generating summaries inside Droids
- selecting the model used by an application compaction hook
- choosing an application checkpoint format
- deleting or rewriting consumer storage
- exposing provider-native compaction blocks as neutral messages
- guaranteeing exact token counts for every provider and attachment type
- compacting incomplete tool loops in the initial implementation
- requiring every consumer to configure compaction

## Related

- [0001: Record architecture decisions](./0001-record-architecture-decisions.md)
- [`../../model.go`](../../model.go)
- [`../../droid.go`](../../droid.go)
- [`../../loop.go`](../../loop.go)
- [`../../storage.go`](../../storage.go)
- [OpenAI compaction](https://developers.openai.com/api/docs/guides/compaction)
- [Anthropic compaction](https://platform.claude.com/docs/en/build-with-claude/compaction)
