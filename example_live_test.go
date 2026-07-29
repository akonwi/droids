//go:build live

// Opt-in live smoke test: hits a real OpenAI-compatible endpoint.
//
//	OPENAI_API_KEY=sk-... go test -tags live -run TestLive -v ./...
//
// Set DROIDS_BASE_URL to route through an AI Gateway or another compatible
// provider, and DROIDS_MODEL to pick the model (default: gpt-4o-mini).
package droids

import (
	"context"
	"os"
	"testing"
)

func TestLive(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("set OPENAI_API_KEY to run the live smoke test")
	}
	model := os.Getenv("DROIDS_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}

	prov, err := NewProviders(OpenAI{
		APIKey:  key,
		BaseURL: os.Getenv("DROIDS_BASE_URL"),
		Models:  []Model{{ID: model, MaxTokens: 256}},
	})
	if err != nil {
		t.Fatal(err)
	}

	weather := NewTool(Tool[struct {
		City string `json:"city"`
	}]{
		Name:        "get_weather",
		Description: "Get the current weather for a city.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		},
		Execute: func(_ context.Context, a struct {
			City string `json:"city"`
		}) (ToolResult, error) {
			return ToolText("It is 22°C and sunny in " + a.City), nil
		},
	})

	d, err := New(Options{
		Providers:    prov,
		Model:        model,
		SystemPrompt: "You are concise.",
		Tools:        []AnyTool{weather},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Stream events in the background for visibility.
	go func() {
		for ev := range d.Events() {
			switch e := ev.(type) {
			case ToolExecutionStart:
				t.Logf("tool call: %s(%s)", e.ToolName, e.Arguments)
			case MessageEnd:
				if am, ok := e.Message.(AssistantMessage); ok {
					t.Logf("assistant: %s (stop=%s, tokens=%d)", textOf(am), am.StopReason, am.Usage.TotalTokens)
				}
			}
		}
	}()

	msg, err := d.Run(context.Background(), "What's the weather in Paris? Use the tool.")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("final: %s", textOf(msg))
	if textOf(msg) == "" {
		t.Fatal("expected a final text answer")
	}
}
