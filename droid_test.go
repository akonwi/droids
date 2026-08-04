package droids

import (
	"context"
	"strings"
	"testing"
)

// fauxProvider is a Provider config backed by a scripted stream, for testing
// the loop without real network calls.
type fauxProvider struct {
	model Model
	reply func(req Request) AssistantMessage
}

func (f fauxProvider) build() (providerEntry, error) {
	m := f.model
	m.Provider = "faux"
	impl := f
	return providerEntry{
		id:     "faux",
		models: map[string]Model{m.ID: m},
		stream: func(ctx context.Context, model Model, req Request, opts callOptions) Stream {
			msg := impl.reply(req)
			msg.Provider = "faux"
			msg.Model = model.ID
			ch := make(chan StreamEvent, 2)
			ch <- StreamStart{Partial: AssistantMessage{Provider: "faux", Model: model.ID}}
			ch <- StreamDone{Message: msg}
			close(ch)
			return &staticStream{events: ch, final: msg}
		},
	}, nil
}

func TestRunSingleTurn(t *testing.T) {
	prov, err := NewProviders(fauxProvider{
		model: Model{ID: "test-model", MaxTokens: 100},
		reply: func(req Request) AssistantMessage {
			return AssistantMessage{
				Content:    []Content{TextContent{Text: "hello back"}},
				StopReason: StopReasonStop,
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	store := NewMemoryStorage()
	d, err := New(Options{
		Providers: prov,
		Model:     "test-model",
		Storage:   store,
		Session:   "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	msg, err := d.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := msg.Text(); got != "hello back" {
		t.Fatalf("got %q", got)
	}

	history, _ := store.Load(context.Background(), "s1")
	if len(history) != 2 { // user + assistant
		t.Fatalf("expected 2 persisted messages, got %d", len(history))
	}
}

func TestNewRejectsDuplicateToolNames(t *testing.T) {
	providers, err := NewProviders(fauxProvider{
		model: Model{ID: "m"},
		reply: func(Request) AssistantMessage {
			return AssistantMessage{StopReason: StopReasonStop}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := func() AnyTool {
		return NewTool(Tool[struct{}]{
			Name: "same",
			Execute: func(context.Context, struct{}) (ToolResult, error) {
				return ToolText("ok"), nil
			},
		})
	}

	_, err = New(Options{
		Providers: providers,
		Model:     "m",
		Tools:     []AnyTool{tool(), tool()},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate tool name "same"`) {
		t.Fatalf("New error = %v", err)
	}
}

func TestRunWithToolCall(t *testing.T) {
	calls := 0
	prov, _ := NewProviders(fauxProvider{
		model: Model{ID: "m"},
		reply: func(req Request) AssistantMessage {
			calls++
			if calls == 1 {
				return AssistantMessage{
					Content: []Content{ToolCall{
						ID:        "c1",
						Name:      "echo",
						Arguments: []byte(`{"text":"world"}`),
					}},
					StopReason: StopReasonToolUse,
				}
			}
			return AssistantMessage{
				Content:    []Content{TextContent{Text: "done"}},
				StopReason: StopReasonStop,
			}
		},
	})

	type echoArgs struct {
		Text string `json:"text"`
	}
	echo := NewTool(Tool[echoArgs]{
		Name: "echo",
		Execute: func(_ context.Context, a echoArgs) (ToolResult, error) {
			return ToolText("echoed: " + a.Text), nil
		},
	})

	d, _ := New(Options{Providers: prov, Model: "m", Tools: []AnyTool{echo}})
	defer d.Close()

	msg, err := d.Run(context.Background(), "call echo")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if msg.Text() != "done" {
		t.Fatalf("got %q", msg.Text())
	}
	if calls != 2 {
		t.Fatalf("expected 2 model calls, got %d", calls)
	}
}
