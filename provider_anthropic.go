package droids

import (
	"context"
	"encoding/json"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// provider_anthropic.go — the Anthropic (Messages API) provider, backed by the
// official anthropic-sdk-go. API-key auth only for now.

// Anthropic configures an Anthropic Messages provider.
type Anthropic struct {
	// APIKey authenticates requests (x-api-key header).
	APIKey string
	// BaseURL overrides the default endpoint (https://api.anthropic.com).
	BaseURL string
	// Headers are extra headers merged into every request.
	Headers map[string]string
	// Models is the catalog this provider serves.
	Models []Model
	// ID overrides the provider id. Default: "anthropic".
	ID string
	// Options are extra SDK request options, applied after the built-ins.
	Options []option.RequestOption
}

func (c Anthropic) build() (providerEntry, error) {
	id := c.ID
	if id == "" {
		id = "anthropic"
	}

	opts := []option.RequestOption{}
	if c.APIKey != "" {
		opts = append(opts, option.WithAPIKey(c.APIKey))
	}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	for k, v := range c.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	opts = append(opts, c.Options...)

	client := anthropic.NewClient(opts...)

	models := map[string]Model{}
	for _, m := range c.Models {
		m.Provider = id
		if m.BaseURL == "" {
			m.BaseURL = c.BaseURL
		}
		models[m.ID] = m
	}

	impl := &anthropicProvider{client: &client}
	return providerEntry{
		id:     id,
		models: models,
		stream: impl.stream,
	}, nil
}

type anthropicProvider struct {
	client *anthropic.Client
}

func (p *anthropicProvider) stream(ctx context.Context, model Model, req Request, _ callOptions) Stream {
	s := &pipeStream{
		events: make(chan StreamEvent, 32),
		done:   make(chan struct{}),
	}
	go p.run(ctx, model, req, s)
	return s
}

func (p *anthropicProvider) run(ctx context.Context, model Model, req Request, s *pipeStream) {
	defer s.finish()

	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = int64(model.MaxTokens)
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model.ID),
		MaxTokens: maxTokens,
		Messages:  toAnthropicMessages(req.Messages),
	}
	if req.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.SystemPrompt}}
	}
	if len(req.Tools) > 0 {
		params.Tools = toAnthropicTools(req.Tools)
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	if budget := thinkingBudget(req.Reasoning); budget > 0 && model.Reasoning {
		// Anthropic requires max_tokens > thinking budget.
		if params.MaxTokens <= budget {
			params.MaxTokens = budget + 1024
		}
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
	}

	partial := AssistantMessage{Provider: model.Provider, Model: model.ID, Timestamp: time.Now().UnixMilli()}
	s.emit(StreamStart{Partial: partial})

	stream := p.client.Messages.NewStreaming(ctx, params)
	var acc anthropic.Message

	for stream.Next() {
		event := stream.Current()
		if err := acc.Accumulate(event); err != nil {
			continue
		}
		p.emitDelta(s, event)
	}

	if err := stream.Err(); err != nil {
		final := AssistantMessage{
			Provider:     model.Provider,
			Model:        model.ID,
			StopReason:   stopReasonForError(ctx),
			ErrorMessage: err.Error(),
			Timestamp:    time.Now().UnixMilli(),
		}
		s.final = final
		s.emit(StreamError{Message: final})
		return
	}

	final := assembleAnthropicMessage(model, acc)
	s.final = final
	s.emit(StreamDone{Message: final})
}

// emitDelta translates one SDK stream event into droids StreamEvents.
func (p *anthropicProvider) emitDelta(s *pipeStream, event anthropic.MessageStreamEventUnion) {
	switch e := event.AsAny().(type) {
	case anthropic.ContentBlockStartEvent:
		idx := int(e.Index)
		switch e.ContentBlock.Type {
		case "text":
			s.emit(StreamTextStart{ContentIndex: idx})
		case "tool_use":
			tu := e.ContentBlock.AsToolUse()
			s.emit(StreamToolCallStart{ContentIndex: idx, ID: tu.ID, Name: tu.Name})
		}
	case anthropic.ContentBlockDeltaEvent:
		idx := int(e.Index)
		switch e.Delta.Type {
		case "text_delta":
			s.emit(StreamTextDelta{ContentIndex: idx, Delta: e.Delta.Text})
		case "input_json_delta":
			s.emit(StreamToolCallDelta{ContentIndex: idx, Delta: e.Delta.PartialJSON})
		case "thinking_delta":
			s.emit(StreamThinkingDelta{ContentIndex: idx, Delta: e.Delta.Thinking})
		}
	}
}

// assembleAnthropicMessage builds the neutral AssistantMessage from the
// accumulated Message.
func assembleAnthropicMessage(model Model, acc anthropic.Message) AssistantMessage {
	msg := AssistantMessage{
		Provider:  model.Provider,
		Model:     model.ID,
		Timestamp: time.Now().UnixMilli(),
	}
	for _, block := range acc.Content {
		switch block.Type {
		case "text":
			msg.Content = append(msg.Content, TextContent{Text: block.AsText().Text})
		case "thinking":
			th := block.AsThinking()
			msg.Content = append(msg.Content, ThinkingContent{Thinking: th.Thinking, Signature: th.Signature})
		case "tool_use":
			tu := block.AsToolUse()
			msg.Content = append(msg.Content, ToolCall{ID: tu.ID, Name: tu.Name, Arguments: []byte(tu.Input)})
		}
	}

	switch acc.StopReason {
	case anthropic.StopReasonToolUse:
		msg.StopReason = StopReasonToolUse
	case anthropic.StopReasonMaxTokens:
		msg.StopReason = StopReasonLength
	default:
		msg.StopReason = StopReasonStop
	}

	msg.Usage = Usage{
		Input:       int(acc.Usage.InputTokens),
		Output:      int(acc.Usage.OutputTokens),
		CacheRead:   int(acc.Usage.CacheReadInputTokens),
		CacheWrite:  int(acc.Usage.CacheCreationInputTokens),
		TotalTokens: int(acc.Usage.InputTokens + acc.Usage.OutputTokens),
	}
	return msg
}

func toAnthropicMessages(messages []Message) []anthropic.MessageParam {
	var out []anthropic.MessageParam
	for _, m := range messages {
		switch msg := m.(type) {
		case UserMessage:
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(textOfContent(msg.Content))))
		case ToolResultMessage:
			// Tool results are carried in a user turn; consecutive user turns
			// are combined by the API.
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(msg.ToolCallID, textOfContent(msg.Content), msg.IsError),
			))
		case AssistantMessage:
			out = append(out, anthropic.NewAssistantMessage(assistantBlocks(msg)...))
		}
	}
	return out
}

func assistantBlocks(msg AssistantMessage) []anthropic.ContentBlockParamUnion {
	var blocks []anthropic.ContentBlockParamUnion
	if txt := textOfContent(msg.Content); txt != "" {
		blocks = append(blocks, anthropic.NewTextBlock(txt))
	}
	for _, tc := range msg.ToolCalls() {
		var input any
		if len(tc.Arguments) > 0 {
			_ = json.Unmarshal(tc.Arguments, &input)
		}
		blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
	}
	return blocks
}

func toAnthropicTools(tools []ToolSchema) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{}
		if props, ok := t.Parameters["properties"]; ok {
			schema.Properties = props
		}
		if req, ok := t.Parameters["required"].([]string); ok {
			schema.Required = req
		} else if req, ok := t.Parameters["required"].([]any); ok {
			schema.Required = toStringSlice(req)
		}
		tp := anthropic.ToolParam{Name: t.Name, InputSchema: schema}
		if t.Description != "" {
			tp.Description = param.NewOpt(t.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out
}

func toStringSlice(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// thinkingBudget maps a reasoning level to an Anthropic thinking token budget.
// 0 disables extended thinking.
func thinkingBudget(level string) int64 {
	switch level {
	case "minimal":
		return 1024
	case "low":
		return 4096
	case "medium":
		return 8192
	case "high", "xhigh":
		return 16384
	default:
		return 0
	}
}
