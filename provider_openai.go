package droids

import (
	"context"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
)

// provider_openai.go — the OpenAI (and OpenAI-compatible) provider, backed by
// the official openai-go SDK. Cloudflare AI Gateway and other compatible
// endpoints are just a custom BaseURL + Headers.

// OpenAI configures an OpenAI-compatible chat-completions provider.
type OpenAI struct {
	// APIKey authenticates requests (sent as a Bearer token).
	APIKey string
	// BaseURL overrides the default endpoint. Point this at an AI Gateway URL
	// to route through Cloudflare. Default: the SDK default (api.openai.com).
	BaseURL string
	// Headers are extra headers merged into every request (e.g. AI Gateway
	// metadata).
	Headers map[string]string
	// Models is the catalog this provider serves.
	Models []Model
	// ID overrides the provider id. Default: "openai".
	ID string
	// Options are extra SDK request options, applied after the built-ins.
	Options []option.RequestOption
}

func (c OpenAI) build() (providerEntry, error) {
	id := c.ID
	if id == "" {
		id = "openai"
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

	client := openai.NewClient(opts...)

	models := map[string]Model{}
	for _, m := range c.Models {
		m.Provider = id
		if m.BaseURL == "" {
			m.BaseURL = c.BaseURL
		}
		models[m.ID] = m
	}

	impl := &openAIProvider{client: &client}
	return providerEntry{
		id:     id,
		models: models,
		stream: impl.stream,
	}, nil
}

type openAIProvider struct {
	client *openai.Client
}

func (p *openAIProvider) stream(ctx context.Context, model Model, req Request, _ callOptions) Stream {
	s := &pipeStream{
		events: make(chan StreamEvent, 32),
		done:   make(chan struct{}),
	}
	go p.run(ctx, model, req, s)
	return s
}

func (p *openAIProvider) run(ctx context.Context, model Model, req Request, s *pipeStream) {
	defer s.finish()

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(model.ID),
		Messages: toOpenAIMessages(req),
	}
	if len(req.Tools) > 0 {
		params.Tools = toOpenAITools(req.Tools)
	}
	if req.MaxTokens > 0 {
		params.MaxCompletionTokens = param.NewOpt(int64(req.MaxTokens))
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	if eff := reasoningEffort(req.Reasoning); eff != "" {
		params.ReasoningEffort = eff
	}
	// Ask for usage in the terminal streamed chunk.
	params.StreamOptions.IncludeUsage = param.NewOpt(true)

	partial := AssistantMessage{Provider: model.Provider, Model: model.ID, Timestamp: time.Now().UnixMilli()}
	s.emit(StreamStart{Partial: partial})

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	acc := openai.ChatCompletionAccumulator{}

	textStarted := false
	seenTool := map[int64]bool{}

	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)

		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			if !textStarted {
				textStarted = true
				s.emit(StreamTextStart{ContentIndex: 0})
			}
			s.emit(StreamTextDelta{ContentIndex: 0, Delta: delta.Content})
		}

		for _, tc := range delta.ToolCalls {
			idx := tc.Index
			if !seenTool[idx] {
				seenTool[idx] = true
				s.emit(StreamToolCallStart{
					ContentIndex: int(idx) + 1,
					ID:           tc.ID,
					Name:         tc.Function.Name,
				})
			}
			if tc.Function.Arguments != "" {
				s.emit(StreamToolCallDelta{
					ContentIndex: int(idx) + 1,
					Delta:        tc.Function.Arguments,
				})
			}
		}
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

	final := assembleMessage(model, acc)
	s.final = final
	s.emit(StreamDone{Message: final})
}

// assembleMessage builds the neutral AssistantMessage from the accumulated
// completion.
func assembleMessage(model Model, acc openai.ChatCompletionAccumulator) AssistantMessage {
	msg := AssistantMessage{
		Provider:   model.Provider,
		Model:      model.ID,
		Timestamp:  time.Now().UnixMilli(),
		StopReason: StopReasonStop,
	}
	if len(acc.Choices) == 0 {
		msg.StopReason = StopReasonError
		msg.ErrorMessage = "no choices in response"
		return msg
	}
	choice := acc.Choices[0]

	if txt := choice.Message.Content; txt != "" {
		msg.Content = append(msg.Content, TextContent{Text: txt})
	}
	for _, tc := range choice.Message.ToolCalls {
		msg.Content = append(msg.Content, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: []byte(tc.Function.Arguments),
		})
	}

	switch choice.FinishReason {
	case "tool_calls":
		msg.StopReason = StopReasonToolUse
	case "length":
		msg.StopReason = StopReasonLength
	default:
		msg.StopReason = StopReasonStop
	}

	u := acc.Usage
	msg.Usage = Usage{
		Input:       int(u.PromptTokens),
		Output:      int(u.CompletionTokens),
		CacheRead:   int(u.PromptTokensDetails.CachedTokens),
		Reasoning:   int(u.CompletionTokensDetails.ReasoningTokens),
		TotalTokens: int(u.TotalTokens),
	}
	return msg
}

func toOpenAIMessages(req Request) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	if req.SystemPrompt != "" {
		out = append(out, openai.SystemMessage(req.SystemPrompt))
	}
	for _, m := range req.Messages {
		switch msg := m.(type) {
		case UserMessage:
			out = append(out, openai.UserMessage(textOfContent(msg.Content)))
		case ToolResultMessage:
			out = append(out, openai.ToolMessage(textOfContent(msg.Content), msg.ToolCallID))
		case AssistantMessage:
			out = append(out, assistantParam(msg))
		}
	}
	return out
}

func assistantParam(msg AssistantMessage) openai.ChatCompletionMessageParamUnion {
	calls := msg.ToolCalls()
	if len(calls) == 0 {
		return openai.AssistantMessage(textOfContent(msg.Content))
	}
	ap := openai.ChatCompletionAssistantMessageParam{}
	if txt := textOfContent(msg.Content); txt != "" {
		ap.Content.OfString = param.NewOpt(txt)
	}
	for _, c := range calls {
		ap.ToolCalls = append(ap.ToolCalls, openai.ChatCompletionMessageToolCallParam{
			ID: c.ID,
			Function: openai.ChatCompletionMessageToolCallFunctionParam{
				Name:      c.Name,
				Arguments: string(c.Arguments),
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &ap}
}

func toOpenAITools(tools []ToolSchema) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  shared.FunctionParameters(t.Parameters),
			},
		})
	}
	return out
}

// textOfContent concatenates the text blocks of a content slice.
func textOfContent(content []Content) string {
	var b []byte
	for _, c := range content {
		if t, ok := c.(TextContent); ok {
			if len(b) > 0 {
				b = append(b, '\n')
			}
			b = append(b, t.Text...)
		}
	}
	return string(b)
}

func reasoningEffort(level string) shared.ReasoningEffort {
	switch level {
	case "minimal", "low", "medium", "high":
		return shared.ReasoningEffort(level)
	case "xhigh":
		return shared.ReasoningEffortHigh
	case "none", "off":
		// Explicitly disable reasoning. Required for reasoning models that would
		// otherwise apply a default effort, which some APIs reject alongside
		// function tools on the chat-completions endpoint.
		return shared.ReasoningEffort("none")
	default:
		return ""
	}
}

func stopReasonForError(ctx context.Context) StopReason {
	if ctx.Err() != nil {
		return StopReasonAborted
	}
	return StopReasonError
}

// pipeStream is a Stream backed by a producer goroutine.
type pipeStream struct {
	events chan StreamEvent
	done   chan struct{}
	final  AssistantMessage
}

func (s *pipeStream) emit(ev StreamEvent) { s.events <- ev }

func (s *pipeStream) finish() {
	close(s.events)
	close(s.done)
}

func (s *pipeStream) Events() <-chan StreamEvent { return s.events }

func (s *pipeStream) Result() AssistantMessage {
	<-s.done
	return s.final
}
