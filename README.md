# droids

A reusable Go agent library. One import gives you a multiprovider LLM
abstraction, a bounded tool-calling loop, and a durable-persistence seam —
composed behind a small `Droid` facade, but usable layer by layer.

```
Storage      durable persistence + observability seam   (Layer 3)
Droid/loop   bounded multi-step tool loop, streaming     (Layer 2)
Providers    model abstraction + multiprovider streaming (Layer 1)
```

Its design distills [pi](https://github.com/earendil-works/pi)'s `pi-ai`
(multiprovider streaming) and `pi-agent-core` (the agent loop) into idiomatic
Go. See [`VISION.md`](./VISION.md) for the rationale and [`AGENTS.md`](./AGENTS.md)
for the architecture invariants.

## Install

```sh
go get github.com/akonwi/droids
```

Requires Go 1.26+.

## Quick start

```go
package main

import (
	"context"
	"fmt"

	"github.com/akonwi/droids"
)

func main() {
	providers, err := droids.NewProviders(droids.OpenAI{APIKey: "sk-..."})
	if err != nil {
		panic(err)
	}

	weather := droids.NewTool(droids.Tool[struct {
		City string `json:"city" jsonschema:"description=City name"`
	}]{
		Name:        "get_weather",
		Description: "Get the current weather for a city.",
		Execute: func(_ context.Context, args struct {
			City string `json:"city" jsonschema:"description=City name"`
		}) (droids.ToolResult, error) {
			return droids.ToolText("22°C and sunny in " + args.City), nil
		},
	})

	d, err := droids.New(droids.Options{
		Providers:    providers,
		Model:        "gpt-4o-mini",
		SystemPrompt: "You are concise.",
		Tools:        []droids.AnyTool{weather},
		MaxTokens:    1024,
	})
	if err != nil {
		panic(err)
	}
	defer d.Close()

	msg, err := d.Execute(context.Background(), "What's the weather in Paris?")
	if err != nil {
		panic(err)
	}
	fmt.Println(msg.Text())
}
```

## Providers

Compose one or more provider configs into a `Providers` registry that routes
each request to the provider that owns the model. Model ids resolve bare
(`"gpt-4o"`) or namespaced (`"openai/gpt-4o"`) to disambiguate.

```go
providers, _ := droids.NewProviders(
	droids.OpenAI{APIKey: openaiKey},
	droids.Anthropic{APIKey: anthropicKey},
)
```

| Provider | Config | Backing SDK |
|----------|--------|-------------|
| OpenAI Responses (and Responses-compatible endpoints) | `droids.OpenAI{}` | `openai-go` |
| Anthropic Messages | `droids.Anthropic{}` | `anthropic-sdk-go` |

Each provider owns an embedded models.dev catalog containing model context and
input/output limits, reasoning support, modalities, and pricing. Callers do not
register model metadata. `OpenAIModels`/`OpenAIModel` and
`AnthropicModels`/`AnthropicModel` expose fresh copies of the built-in entries.

Refresh the registered catalogs explicitly when current models are needed:

```go
if err := providers.RefreshModels(ctx); err != nil {
	// The embedded catalog remains usable.
}
```

Refreshes fetch `https://models.dev/api.json`, overlay complete entries, and
leave the previous catalog unchanged on failure. Maintainers update the
embedded fallback with `go generate ./...`. A refresh affects subsequent
model resolutions and newly created Droids; an existing Droid retains the model
metadata resolved when it was created. Point `OpenAI.BaseURL` at a proxy for the
OpenAI Responses model family, and `Anthropic.BaseURL` at a proxy for the
Anthropic Messages model family.

### Cloudflare AI Gateway

The gateway is transport in front of the real providers, so it's a decorator
over the provider configs. The native Cloudflare `openai` endpoint supports the
Responses API and retains the OpenAI model catalog:

```go
gw := droids.CloudflareGateway{AccountID: "...", GatewayID: "...", Token: "..."}

providers, _ := droids.NewProviders(
	gw.OpenAI(droids.OpenAI{APIKey: openaiKey}),
	gw.Anthropic(droids.Anthropic{APIKey: anthropicKey}),
)

```

## Images and files

User messages can carry unnamed images and named files from HTTPS or inline
data URLs. Applications remain responsible for storing, authorizing, and
resolving durable attachment records before constructing messages.

```go
report, err := droids.NewFileURL(
	"report.pdf",
	"application/pdf",
	"https://files.example/report.pdf?signature=...",
)
if err != nil {
	return err
}

message := droids.UserMessage{Content: []droids.Content{
	droids.TextContent{Text: "Summarize this report."},
	report,
	droids.NewImageData("image/png", imageBytes),
}}
run, err := droid.StreamMessage(ctx, message)
```

OpenAI Responses maps named images to image inputs and other files to file
inputs. Provider/model attachment support remains authoritative; unsupported
content produces a `StreamError` rather than being discarded. Signed URLs
should be regenerated when rebuilding a transcript, not persisted as durable
application attachment identifiers.

## Tools

Tools are generic over their argument type; the JSON Schema is derived from the
struct by reflection (override with `Parameters`). Arguments are JSON-decoded
before `Execute`.

```go
type Args struct {
	Path string `json:"path" jsonschema:"description=File path"`
}

read := droids.NewTool(droids.Tool[Args]{
	Name:        "read_file",
	Description: "Read a text file.",
	Execute: func(ctx context.Context, a Args) (droids.ToolResult, error) {
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return droids.ToolResult{}, err
		}
		return droids.ToolText(string(data)), nil
	},
})
```

Batches of tool calls execute in **parallel** by default; mark a tool
`Mode: droids.ModeSequential` (or set `Options.ToolExecution`) to serialize.
Tool results are always appended to the transcript in the model's requested
order, while live events fire in completion order. Set `ToolResult.IsError` for
an application-level failure whose content and details should still be
preserved for the model and observers.

### MCP namespaces

The optional `github.com/akonwi/droids/mcp` package exposes each configured MCP
server as one progressively disclosed namespace tool. The model initially sees
small tools such as `mcp_github` rather than every remote schema, then uses
`list`, `search`, `describe`, and `call` within the chosen namespace.
Connections and tool metadata are loaded lazily.

```go
manager, err := droidsmcp.NewManager(droidsmcp.Server{
	Name:        "github",
	Description: "GitHub repositories, issues, and pull requests",
	Transport: func(ctx context.Context) (sdkmcp.Transport, error) {
		return &sdkmcp.StreamableClientTransport{
			Endpoint:     "https://example.com/mcp",
			OAuthHandler: oauthHandler, // configured and persisted by the application
		}, nil
	},
})
if err != nil {
	panic(err)
}
defer manager.Close()

d, err := droids.New(droids.Options{
	Providers: providers,
	Model:     "gpt-4o-mini",
	Tools:     append(localTools, manager.Tools()...),
})
```

The package uses the official MCP Go SDK and accepts any SDK transport. MCP
transports are single-use, so `Server.Transport` is a factory. OAuth browser
interaction and credential persistence remain application-owned; provide a
configured SDK `OAuthHandler` through the transport factory.

### Hooks

`BeforeToolCall` can reject a call or short-circuit it with a cached result;
`AfterToolCall` can rewrite the result. Hook errors degrade to an error tool
result rather than crashing the loop. A rejection becomes an error tool result
that the model observes, and the run continues.

To reject this call and also stop before another model turn, return
`Reject: true` and cancel the run context or call `Abort()` inside the hook;
cancelling after observing an event may be too late. Cancellation is
cooperative and does not stop already-running or same-batch tools that ignore
their context. The rejection result is persisted, so reconstruct the desired
transcript before using `Continue` rather than appending a second result for the
same call.

```go
d, _ := droids.New(droids.Options{
	// ...
	BeforeToolCall: func(ctx context.Context, in droids.BeforeToolContext) (droids.BeforeToolResult, error) {
		if !allowed(in.ToolCall.Name) {
			return droids.BeforeToolResult{Reject: true, Reason: "not permitted"}, nil
		}
		return droids.BeforeToolResult{}, nil
	},
})
```

## Streaming vs one-shot

`Execute` blocks and returns the final message — ideal for synchronous,
non-streamed work. To stream one run, use `Stream`; the returned `Run` event
channel closes when that run ends, so an ordinary range loop is sufficient:

```go
run, err := d.Stream(ctx, "message ...")
if err != nil {
	return err
}

for ev := range run.Events() {
	switch e := ev.(type) {
	case droids.MessageDelta:
		if td, ok := e.Stream.(droids.StreamTextDelta); ok {
			fmt.Print(td.Delta)
		}
	case droids.ToolExecutionEnd:
		fmt.Printf("tool %s done\n", e.ToolName)
	case droids.TurnEnd:
		// one assistant turn + its tool results
	case droids.ErrorEvent:
		fmt.Println("error:", e.Message.ErrorMessage)
	}
}

message, err := run.Result()
```

For a long-lived interactive session, use `Send`/`Steer`/`Abort` with
`Droid.Events()`:

```go
events := d.Events()
d.Send(ctx, "message ...")       // starts a run, or queues behind one
d.Steer("actually, focus on X")  // inject between turns of the active run
d.Abort()                         // interrupt the active run
```

`Droid.Events()` is session-scoped: it returns the same channel on every call
and closes only when the droid finishes shutting down. Stop consuming each run
on `AgentEnd`; do not range over it expecting per-run closure. `Stream` events
are delivered to the returned `Run` rather than `Droid.Events()`. If neither
event API is requested, the loop still runs and persists.

## Storage

`Storage` (`Load` + `Append`) persists and rehydrates a session's transcript.
It defaults to in-memory; back it with your own implementation for durable
runs. The loop appends on every completed message, independent of the event
channel.

```go
d, _ := droids.New(droids.Options{
	Providers: providers,
	Model:     "gpt-4o-mini",
	Storage:   myPostgresStore, // implements droids.Storage
	Session:   "run-42",        // rehydrates on New
})
```

## Context compaction

Droids can detect context pressure from the resolved model's `ContextWindow`,
the rendered request, and reserved output capacity. Applications may provide an
optional compaction hook that returns a smaller active transcript:

```go
d, err := droids.New(droids.Options{
	Providers: providers,
	Model:     "openai/gpt-5.4",
	MaxTokens: 16_000,
	Compact: func(ctx context.Context, req droids.CompactionRequest) (droids.CompactionResult, error) {
		summary, err := summarizeAndPersist(ctx, req.Messages)
		if err != nil {
			return droids.CompactionResult{}, err
		}
		return droids.CompactionResult{
			Applied: true,
			Messages: append([]droids.Message{summary}, recentTail(req.Messages)...),
		}, nil
	},
})
```

`Model.ContextWindow`, `Model.MaxInputTokens`, and `Model.MaxOutputTokens` are
provider capabilities sourced from the catalog. `Options.MaxTokens` is the
actual per-request output allowance and is capped by `MaxOutputTokens`. Zero
defaults to 4096; reasoning modes requiring a larger allowance must set it
explicitly.

The hook runs proactively before the first provider request of a normal run
when estimated input plus reserved output reaches 80% of the model context
window, or when estimated input reaches 80% of a stricter input-only limit. It
may also run once after a provider-confirmed input context overflow, followed
by one retry. Omit `Compact` to keep the existing provider behavior.

Droids replaces only its active in-memory context. It never deletes or rewrites
`Storage`; durable applications own checkpoint persistence and should make
future `Storage.Load` calls reconstruct the checkpoint plus its raw tail. Hook
implementations may use another in-memory Droid with a fast summarization model,
but that Droid should omit `Compact` to avoid recursive compaction.

`CompactionStart` and `CompactionEnd` events expose context estimates and whether
a replacement was applied without exposing summary content. Estimates are
provider-neutral and currently approximate. The embedded catalogs provide the
limits required for proactive detection; provider-overflow recovery remains the
fallback.

## Human-in-the-loop continuation

After userland approves and executes a gated tool, persist its
`ToolResultMessage`, close the prior droid (or reconstruct in a new process),
then reconstruct the droid so it reloads the transcript and continue without
injecting another user message:

```go
err := store.Append(ctx, sessionID, droids.ToolResultMessage{
	ToolCallID: approvedCall.ID,
	ToolName:   approvedCall.Name,
	Content:    []droids.Content{droids.TextContent{Text: resultText}},
})
if err != nil {
	return err
}

d, err := droids.New(droids.Options{
	Providers: providers,
	Model:     model,
	Storage:   store,
	Session:   sessionID,
})
if err != nil {
	return err
}
defer d.Close()

if wantEvents {
	run, err := d.ContinueStream(ctx)
	if err != nil {
		return err
	}
	for event := range run.Events() {
		handle(event)
	}
	message, err := run.Result()
	// use message
} else {
	message, err := d.Continue(ctx)
	// use message
}
```

The transcript must end with a complete, source-ordered batch of tool results
matching every tool call in the preceding assistant message. Continuations get
a fresh `MaxSteps` budget. Pending steering is retained for the next normal
`Execute`/`Stream` run rather than injected into the continuation.

## Example

A CLI chat REPL with read-only filesystem tools lives in
[`cmd/chat`](./cmd/chat):

```sh
OPENAI_API_KEY=sk-...    go run ./cmd/chat
ANTHROPIC_API_KEY=sk-... go run ./cmd/chat
```

The chat example includes the official MCP everything test server and requires
Node.js with `npx`. Try: `Search the everything MCP namespace for an echo tool,
describe it, then call it with hello from droids.` The server starts lazily on
the first namespace operation and exits when chat closes.

## License

TBD.
