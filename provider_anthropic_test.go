package droids

import (
	"encoding/json"
	"strings"
	"testing"
)

// Verify neutral messages/tools translate to the Anthropic wire shape.
func TestAnthropicMessageConversion(t *testing.T) {
	msgs := []Message{
		UserMessage{Content: []Content{TextContent{Text: "hi"}}},
		AssistantMessage{Content: []Content{
			TextContent{Text: "let me check"},
			ToolCall{ID: "t1", Name: "get_weather", Arguments: []byte(`{"city":"Paris"}`)},
		}},
		ToolResultMessage{ToolCallID: "t1", ToolName: "get_weather", Content: []Content{TextContent{Text: "sunny"}}},
	}

	params := toAnthropicMessages(msgs)
	if len(params) != 3 {
		t.Fatalf("got %d messages, want 3", len(params))
	}

	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"role":"user"`,
		`"role":"assistant"`,
		`"type":"tool_use"`,
		`"name":"get_weather"`,
		`"city":"Paris"`,
		`"type":"tool_result"`,
		`"tool_use_id":"t1"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("wire payload missing %q\n%s", want, s)
		}
	}
}

func TestAnthropicToolConversion(t *testing.T) {
	tools := []ToolSchema{{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []any{"city"},
		},
	}}
	raw, err := json.Marshal(toAnthropicTools(tools))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"name":"get_weather"`, `"input_schema"`, `"city"`, `"required":["city"]`} {
		if !strings.Contains(s, want) {
			t.Fatalf("tool payload missing %q\n%s", want, s)
		}
	}
}

func TestThinkingBudget(t *testing.T) {
	if thinkingBudget("off") != 0 {
		t.Fatal("off should disable thinking")
	}
	if thinkingBudget("high") <= thinkingBudget("low") {
		t.Fatal("high budget should exceed low")
	}
}
