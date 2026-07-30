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
	providers, err := droids.NewProviders(droids.OpenAI{
		APIKey: "sk-...",
		Models: []droids.Model{{ID: "gpt-4o-mini", MaxTokens: 1024}},
	})
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
	})
	if err != nil {
		panic(err)
	}
	defer d.Close()

	msg, err := d.Run(context.Background(), "What's the weather in Paris?")
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
	droids.OpenAI{APIKey: openaiKey, Models: []droids.Model{{ID: "gpt-4o"}}},
	droids.Anthropic{APIKey: anthropicKey, Models: []droids.Model{{ID: "claude-3-5-sonnet-latest"}}},
)
```

| Provider | Config | Backing SDK |
|----------|--------|-------------|
| OpenAI (and OpenAI-compatible) | `droids.OpenAI{}` | `openai-go` |
| Anthropic Messages | `droids.Anthropic{}` | `anthropic-sdk-go` |

Both take `APIKey`, `BaseURL`, `Headers`, and `Models`. Point `BaseURL` at any
compatible endpoint.

### Cloudflare AI Gateway

The gateway is transport in front of the real providers, so it's a decorator
over the provider configs:

```go
gw := droids.CloudflareGateway{AccountID: "...", GatewayID: "...", Token: "..."}

providers, _ := droids.NewProviders(
	gw.OpenAI(droids.OpenAI{APIKey: openaiKey, Models: ...}),
	gw.Anthropic(droids.Anthropic{APIKey: anthropicKey, Models: ...}),
)

// any OpenAI-compatible upstream by gateway slug:
gw.OpenAICompatible("groq", droids.OpenAI{APIKey: groqKey, ID: "groq", Models: ...})

// or the unified compat endpoint (upstream chosen by model id):
providers, _ := droids.NewProviders(gw.Compat(droids.OpenAI{
	APIKey: key,
	Models: []droids.Model{{ID: "anthropic/claude-3-5-sonnet"}},
}))
```

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
order, while live events fire in completion order.

### Hooks

`BeforeToolCall` can block a call or short-circuit it with a cached result;
`AfterToolCall` can rewrite the result. Hook errors degrade to an error tool
result rather than crashing the loop.

```go
d, _ := droids.New(droids.Options{
	// ...
	BeforeToolCall: func(ctx context.Context, in droids.BeforeToolContext) (droids.BeforeToolResult, error) {
		if !allowed(in.ToolCall.Name) {
			return droids.BeforeToolResult{Block: true, Reason: "not permitted"}, nil
		}
		return droids.BeforeToolResult{}, nil
	},
})
```

## Streaming vs one-shot

`Run` blocks and returns the final message — ideal for one-shot / background
work. For interactive use, drive the droid with `Send`/`Steer`/`Abort` and range
over its long-lived event channel:

```go
d.Send(ctx, "message ...")       // starts a turn, or a follow-up if busy
d.Steer("actually, focus on X")  // inject mid-run, picked up between turns
d.Abort()                         // interrupt the active run

for ev := range d.Events() {
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
```

`Events()` returns the same channel on every call; if you never call it, the
loop still runs and still persists.

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

## Example

A CLI chat REPL with read-only filesystem tools lives in
[`cmd/chat`](./cmd/chat):

```sh
OPENAI_API_KEY=sk-...    go run ./cmd/chat
ANTHROPIC_API_KEY=sk-... go run ./cmd/chat
```

## License

TBD.
