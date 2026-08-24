# 0004: Add provider-owned model catalogs

## Status

Proposed

## Context

OpenAI and Anthropic provider configs require callers to supply `[]Model`. This makes every application responsible for model IDs, context limits, output limits, reasoning support, modalities, and pricing. Incomplete declarations are common and prevent model-aware behavior such as proactive context compaction.

Provider model-list endpoints are not sufficient metadata authorities. They generally expose identifiers and display metadata but not reliable context, input, output, pricing, or reasoning capabilities.

Pi-ai solves this with provider-owned generated catalogs whose primary upstream is `https://models.dev/api.json`. Its model registry also supports refreshing provider catalogs dynamically while retaining a static baseline.

Droids is still young and can accept a breaking provider configuration change.

## Decision

OpenAI and Anthropic providers own their model catalogs. Remove `Models []Model` from both provider configs.

Registering a provider immediately registers its embedded baseline catalog:

```go
providers, err := droids.NewProviders(
    droids.OpenAI{APIKey: openAIKey},
    droids.Anthropic{APIKey: anthropicKey},
)
```

Resolve models through the existing registry API:

```go
model, ok := providers.Model("openai/gpt-4o-mini")
```

Expose fresh copies of built-in entries for inspection:

```go
OpenAIModels() []Model
OpenAIModel(id string) (Model, bool)
AnthropicModels() []Model
AnthropicModel(id string) (Model, bool)
```

### Embedded catalog

Embed an OpenAI and Anthropic snapshot derived from models.dev. Only register entries that:

- have a model ID
- declare tool-call support
- declare positive context and output limits
- support text output

Translate models.dev metadata into Droids' neutral `Model` fields. The embedded snapshot is always available and does not require startup network access.

Provider configs apply their effective provider ID and base URL to catalog entries. This allows a Cloudflare AI Gateway decorator to use the upstream OpenAI or Anthropic catalog while preserving gateway routing metadata.

### Dynamic refresh

Add this method to `Providers`:

```go
RefreshModels(ctx context.Context) error
```

Refresh explicitly fetches `https://models.dev/api.json`, validates and translates complete entries, and overlays them by model ID onto each registered built-in provider's baseline.

A refresh:

- does not run implicitly during provider construction
- updates all eligible providers from one fetched document
- retains embedded entries absent from the response
- ignores incomplete or unsupported entries
- applies updates atomically under the registry lock
- leaves the current catalog unchanged when fetching or parsing fails
- affects subsequent registry model resolutions and newly created Droids; existing Droids retain their resolved model snapshot

The initial implementation keeps refreshed metadata in memory. Persistent catalog caching and HTTP validator support can be added behind the same API later.

### Model limits

Separate model capabilities from request configuration:

```go
type Model struct {
    API              ModelAPI
    Reasoning        bool
    ReasoningLevels  []string // explicit Droids levels supported by this API implementation
    ContextWindow    int // combined input and output capacity
    MaxInputTokens   int // optional stricter input-only capability
    MaxOutputTokens  int // maximum provider output capability
}

type Options struct {
    MaxTokens int // actual per-request output allowance
}
```

`Options.MaxTokens` defaults to 4096 and is capped by `Model.MaxOutputTokens`. Droids rejects an explicit allowance above the model capability. A reasoning mode that requires more than the default allowance returns a configuration error and requires the caller to opt into a larger value.

Compaction reserves the actual per-request output allowance rather than the model's maximum output capability. It triggers when either:

```text
estimated input + reserved output >= 80% of ContextWindow
```

or:

```text
estimated input >= 80% of MaxInputTokens
```

The second condition applies only when the catalog declares a stricter input-only limit.

### Custom endpoints

`OpenAI.BaseURL`, `Anthropic.BaseURL`, and provider ID overrides remain supported for proxies that expose the corresponding upstream model family. They change routing metadata but retain the upstream provider catalog. Remove the broad Cloudflare `OpenAICompatible` and legacy `Compat` decorators because unrelated model families cannot be represented soundly without a custom catalog. A future explicit custom-provider/catalog-source abstraction can support those endpoints.

## Consequences

Positive:

- applications register providers rather than reconstructing provider metadata
- proactive compaction works without caller-supplied context windows
- built-in models are available offline and at startup
- explicit refresh discovers newer complete model entries without a library release
- actual output allowances no longer conflate request behavior with model capability
- model lookup and routing remain provider-neutral
- gateway decorators inherit upstream model capabilities automatically

Tradeoffs:

- removing provider `Models` fields is a breaking API change
- adding `RefreshModels(context.Context) error` to `Providers` requires external registry implementations and test doubles to add the method; implementations without dynamic catalogs may return nil
- the embedded snapshot can become stale until refreshed or the library updates
- runtime refresh depends on the availability and accuracy of models.dev
- refreshed catalogs are not persisted in the initial implementation
- provider-native model-list endpoints are not used as metadata authorities
- arbitrary Responses-compatible endpoints no longer inject custom model metadata through `OpenAI.Models`
- the broad Cloudflare `OpenAICompatible` and legacy `Compat` decorators are removed
- catalog corrections may eventually require a Droids-specific override layer

## Related

- [0001: Record architecture decisions](./0001-record-architecture-decisions.md)
- [0003: Add an optional application-managed compaction hook](./0003-optional-compaction-hook.md)
- [`../../model.go`](../../model.go)
- [`../../model_catalog.go`](../../model_catalog.go)
- [`../../provider.go`](../../provider.go)
- [models.dev](https://models.dev)
- [pi-ai model generation](https://github.com/earendil-works/pi/blob/main/packages/ai/scripts/generate-models.ts)
