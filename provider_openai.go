package droids

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// provider_openai.go — the OpenAI Responses provider, backed by the official
// openai-go SDK. Cloudflare AI Gateway and other Responses-compatible
// endpoints are just a custom BaseURL + Headers.

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAI configures an OpenAI Responses provider.
type OpenAI struct {
	// APIKey authenticates requests (sent as a Bearer token).
	APIKey string
	// BaseURL overrides the default endpoint. Point this at an AI Gateway URL
	// to route through Cloudflare. Default: the SDK default (api.openai.com).
	BaseURL string
	// Headers are extra headers merged into every request (e.g. AI Gateway
	// metadata).
	Headers map[string]string
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

	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	models := map[string]Model{}
	for _, model := range catalogModels(builtinModelCatalog, "openai", id) {
		model.BaseURL = baseURL
		models[model.ID] = model
	}

	impl := &openAIProvider{client: &client}
	return providerEntry{
		id:        id,
		catalogID: "openai",
		baseURL:   baseURL,
		models:    models,
		stream:    impl.stream,
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

	partial := AssistantMessage{Provider: model.Provider, Model: model.ID, Timestamp: time.Now().UnixMilli()}
	s.emit(StreamStart{Partial: partial})

	if err := validateReasoning(model, req.Reasoning); err != nil {
		final := responseErrorMessage(model, ctx, err.Error())
		s.final = final
		s.emit(StreamError{Message: final})
		return
	}

	input, err := toOpenAIInput(req.Messages)
	if err != nil {
		final := responseErrorMessage(model, ctx, err.Error())
		s.final = final
		s.emit(StreamError{Message: final})
		return
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(model.ID),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		// Droids owns and replays the durable transcript. Do not couple its
		// storage lifetime to provider-side response retention.
		Store: param.NewOpt(false),
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
	}
	if req.SystemPrompt != "" {
		params.Instructions = param.NewOpt(req.SystemPrompt)
	}
	if len(req.Tools) > 0 {
		params.Tools = toOpenAITools(req.Tools)
	}
	if req.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(req.MaxTokens))
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	if eff := reasoningEffort(req.Reasoning); eff != "" && model.Reasoning {
		params.Reasoning.Effort = eff
		if eff != shared.ReasoningEffortNone {
			params.Reasoning.Summary = shared.ReasoningSummaryAuto
		}
	}

	stream := p.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	textStarted := map[int64]bool{}
	seenTool := map[int64]bool{}

	for stream.Next() {
		event := stream.Current()
		switch e := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			emitOpenAITextDelta(s, textStarted, e.OutputIndex, e.Delta)

		case responses.ResponseRefusalDeltaEvent:
			emitOpenAITextDelta(s, textStarted, e.OutputIndex, e.Delta)

		case responses.ResponseReasoningSummaryTextDeltaEvent:
			s.emit(StreamThinkingDelta{ContentIndex: int(e.OutputIndex), Delta: e.Delta})

		case responses.ResponseReasoningTextDeltaEvent:
			s.emit(StreamThinkingDelta{ContentIndex: int(e.OutputIndex), Delta: e.Delta})

		case responses.ResponseOutputItemAddedEvent:
			call, ok := e.Item.AsAny().(responses.ResponseFunctionToolCall)
			if !ok {
				continue
			}
			if call.CallID == "" {
				final := responseErrorMessage(model, ctx, "OpenAI function call is missing call_id")
				s.final = final
				s.emit(StreamError{Message: final})
				return
			}
			seenTool[e.OutputIndex] = true
			s.emit(StreamToolCallStart{
				ContentIndex: int(e.OutputIndex),
				ID:           call.CallID,
				Name:         call.Name,
			})

		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			// The output-item-added event precedes argument deltas and carries
			// the call id/name needed by StreamToolCallStart.
			if seenTool[e.OutputIndex] {
				s.emit(StreamToolCallDelta{ContentIndex: int(e.OutputIndex), Delta: e.Delta})
			}

		case responses.ResponseCompletedEvent:
			final := assembleResponse(model, e.Response)
			s.final = final
			if final.StopReason == StopReasonError || final.StopReason == StopReasonAborted {
				s.emit(StreamError{Message: final})
			} else {
				s.emit(StreamDone{Message: final})
			}
			return

		case responses.ResponseIncompleteEvent:
			final := assembleResponse(model, e.Response)
			s.final = final
			if final.StopReason == StopReasonError {
				s.emit(StreamError{Message: final})
			} else {
				s.emit(StreamDone{Message: final})
			}
			return

		case responses.ResponseFailedEvent:
			final := assembleResponse(model, e.Response)
			if ctx.Err() != nil {
				final.StopReason = StopReasonAborted
			}
			if final.ErrorMessage == "" {
				final.ErrorMessage = "OpenAI response failed"
			}
			s.final = final
			s.emit(StreamError{Message: final})
			return

		case responses.ResponseErrorEvent:
			final := responseErrorMessageWithCode(model, ctx, e.Code, e.Message)
			s.final = final
			s.emit(StreamError{Message: final})
			return
		}
	}

	if err := stream.Err(); err != nil {
		final := responseErrorMessageWithCode(model, ctx, "", err.Error())
		s.final = final
		s.emit(StreamError{Message: final})
		return
	}

	final := responseErrorMessage(model, ctx, "response stream ended without a terminal event")
	s.final = final
	s.emit(StreamError{Message: final})
}

func emitOpenAITextDelta(s *pipeStream, started map[int64]bool, outputIndex int64, delta string) {
	if !started[outputIndex] {
		started[outputIndex] = true
		s.emit(StreamTextStart{ContentIndex: int(outputIndex)})
	}
	s.emit(StreamTextDelta{ContentIndex: int(outputIndex), Delta: delta})
}

// assembleResponse builds the neutral AssistantMessage from a terminal
// Responses API object. Raw reasoning and output-message items are retained as
// opaque signatures so stateless transcript replay preserves their identity
// and encrypted reasoning content.
func assembleResponse(model Model, response responses.Response) AssistantMessage {
	msg := AssistantMessage{
		Provider:      model.Provider,
		Model:         model.ID,
		ResponseModel: string(response.Model),
		ResponseID:    response.ID,
		Timestamp:     time.Now().UnixMilli(),
		StopReason:    StopReasonStop,
	}

	hasToolCall := false
	for _, item := range response.Output {
		switch output := item.AsAny().(type) {
		case responses.ResponseOutputMessage:
			msg.Content = append(msg.Content, TextContent{
				Text:      responseOutputText(output),
				Signature: output.RawJSON(),
			})
		case responses.ResponseReasoningItem:
			msg.Content = append(msg.Content, ThinkingContent{
				Thinking:  responseReasoningText(output),
				Signature: output.RawJSON(),
			})
		case responses.ResponseFunctionToolCall:
			if output.CallID == "" {
				msg.StopReason = StopReasonError
				msg.ErrorMessage = "OpenAI function call is missing call_id"
				continue
			}
			hasToolCall = true
			msg.Content = append(msg.Content, ToolCall{
				ID:        output.CallID,
				Name:      output.Name,
				Arguments: []byte(output.Arguments),
			})
		}
	}

	// Terminal status takes precedence over any partial function-call item. An
	// incomplete response must never cause the loop to execute truncated args.
	if msg.StopReason == StopReasonError {
		// Preserve translation/protocol errors set while assembling output.
	} else if response.Status == responses.ResponseStatusIncomplete {
		switch response.IncompleteDetails.Reason {
		case "max_output_tokens":
			msg.StopReason = StopReasonLength
		default:
			msg.StopReason = StopReasonError
			msg.ErrorMessage = "incomplete response: " + response.IncompleteDetails.Reason
		}
	} else if response.Status == responses.ResponseStatusCancelled {
		msg.StopReason = StopReasonAborted
		msg.ErrorMessage = response.Error.Message
		if msg.ErrorMessage == "" {
			msg.ErrorMessage = "OpenAI response cancelled"
		}
	} else if response.Status == responses.ResponseStatusFailed {
		msg.StopReason = StopReasonError
		msg.ErrorMessage = response.Error.Message
		if isContextWindowError(string(response.Error.Code), response.Error.Message) {
			msg.StopReason = StopReasonContextWindow
		}
	} else if hasToolCall {
		msg.StopReason = StopReasonToolUse
	}

	u := response.Usage
	msg.Usage = Usage{
		Input:       int(u.InputTokens),
		Output:      int(u.OutputTokens),
		CacheRead:   int(u.InputTokensDetails.CachedTokens),
		Reasoning:   int(u.OutputTokensDetails.ReasoningTokens),
		TotalTokens: int(u.TotalTokens),
	}
	return msg
}

func responseOutputText(message responses.ResponseOutputMessage) string {
	var text strings.Builder
	for _, part := range message.Content {
		switch content := part.AsAny().(type) {
		case responses.ResponseOutputText:
			text.WriteString(content.Text)
		case responses.ResponseOutputRefusal:
			text.WriteString(content.Refusal)
		}
	}
	return text.String()
}

func responseReasoningText(item responses.ResponseReasoningItem) string {
	parts := make([]string, 0, len(item.Content)+len(item.Summary))
	for _, content := range item.Content {
		if content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	if len(parts) == 0 {
		for _, summary := range item.Summary {
			if summary.Text != "" {
				parts = append(parts, summary.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func responseErrorMessage(model Model, ctx context.Context, message string) AssistantMessage {
	return responseErrorMessageWithCode(model, ctx, "", message)
}

func responseErrorMessageWithCode(model Model, ctx context.Context, code, message string) AssistantMessage {
	reason := stopReasonForError(ctx)
	if reason != StopReasonAborted && isContextWindowError(code, message) {
		reason = StopReasonContextWindow
	}
	return AssistantMessage{
		Provider:     model.Provider,
		Model:        model.ID,
		StopReason:   reason,
		ErrorMessage: message,
		Timestamp:    time.Now().UnixMilli(),
	}
}

func toOpenAIInput(messages []Message) (responses.ResponseInputParam, error) {
	var out responses.ResponseInputParam
	for _, message := range messages {
		switch msg := message.(type) {
		case UserMessage:
			content, err := openAIUserContent(msg.Content)
			if err != nil {
				return nil, err
			}
			if len(content) > 0 {
				out = append(out, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser))
			}
		case ToolResultMessage:
			content, err := openAIToolOutput(msg.Content)
			if err != nil {
				return nil, err
			}
			if len(content) > 0 {
				out = append(out, responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, content))
			} else {
				out = append(out, responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, ""))
			}
		case AssistantMessage:
			content, err := openAIAssistantInput(msg)
			if err != nil {
				return nil, err
			}
			out = append(out, content...)
		}
	}
	return out, nil
}

func openAIUserContent(content []Content) (responses.ResponseInputMessageContentListParam, error) {
	out := make(responses.ResponseInputMessageContentListParam, 0, len(content))
	for i, block := range content {
		switch value := block.(type) {
		case TextContent:
			out = append(out, responses.ResponseInputContentParamOfInputText(value.Text))
		case ImageContent:
			image, err := openAIImageParam(value)
			if err != nil {
				return nil, openAIContentError("user", i, "ImageContent", err)
			}
			out = append(out, responses.ResponseInputContentUnionParam{OfInputImage: &image})
		case FileContent:
			if err := validateFileContent(value); err != nil {
				return nil, openAIContentError("user", i, "FileContent", err)
			}
			if isImageMediaType(value.MediaType) {
				image, err := openAIImageParam(ImageContent{MediaType: value.MediaType, URL: value.URL})
				if err != nil {
					return nil, openAIContentError("user", i, "FileContent", err)
				}
				out = append(out, responses.ResponseInputContentUnionParam{OfInputImage: &image})
				continue
			}
			file, err := openAIFileParam(value)
			if err != nil {
				return nil, openAIContentError("user", i, "FileContent", err)
			}
			out = append(out, responses.ResponseInputContentUnionParam{OfInputFile: &file})
		default:
			return nil, openAIContentError("user", i, fmt.Sprintf("%T", block), fmt.Errorf("content type is not supported"))
		}
	}
	return out, nil
}

func openAIToolOutput(content []Content) (responses.ResponseFunctionCallOutputItemListParam, error) {
	out := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(content))
	for i, block := range content {
		switch value := block.(type) {
		case TextContent:
			out = append(out, responses.ResponseFunctionCallOutputItemParamOfInputText(value.Text))
		case ImageContent:
			image, err := openAIToolImageParam(value)
			if err != nil {
				return nil, openAIContentError("tool result", i, "ImageContent", err)
			}
			out = append(out, responses.ResponseFunctionCallOutputItemUnionParam{OfInputImage: &image})
		case FileContent:
			if err := validateFileContent(value); err != nil {
				return nil, openAIContentError("tool result", i, "FileContent", err)
			}
			if isImageMediaType(value.MediaType) {
				image, err := openAIToolImageParam(ImageContent{MediaType: value.MediaType, URL: value.URL})
				if err != nil {
					return nil, openAIContentError("tool result", i, "FileContent", err)
				}
				out = append(out, responses.ResponseFunctionCallOutputItemUnionParam{OfInputImage: &image})
				continue
			}
			file, err := openAIToolFileParam(value)
			if err != nil {
				return nil, openAIContentError("tool result", i, "FileContent", err)
			}
			out = append(out, responses.ResponseFunctionCallOutputItemUnionParam{OfInputFile: &file})
		default:
			return nil, openAIContentError("tool result", i, fmt.Sprintf("%T", block), fmt.Errorf("content type is not supported"))
		}
	}
	return out, nil
}

func openAIAssistantInput(msg AssistantMessage) ([]responses.ResponseInputItemUnionParam, error) {
	var out []responses.ResponseInputItemUnionParam
	for i, block := range msg.Content {
		switch value := block.(type) {
		case ThinkingContent:
			if reasoning, ok := signedReasoningItem(value.Signature); ok {
				out = append(out, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
			}
		case TextContent:
			if message, ok := signedOutputMessage(value.Signature); ok {
				out = append(out, responses.ResponseInputItemUnionParam{OfOutputMessage: &message})
			} else if value.Text != "" {
				out = append(out, responses.ResponseInputItemParamOfMessage(value.Text, responses.EasyInputMessageRoleAssistant))
			}
		case ToolCall:
			// Partial calls are retained on incomplete messages for observability,
			// but replaying one without a matching output corrupts Responses input.
			if msg.StopReason == StopReasonToolUse {
				out = append(out, responses.ResponseInputItemParamOfFunctionCall(string(value.Arguments), value.ID, value.Name))
			}
		default:
			return nil, openAIContentError("assistant", i, fmt.Sprintf("%T", block), fmt.Errorf("content type is not supported"))
		}
	}
	return out, nil
}

func openAIImageParam(image ImageContent) (responses.ResponseInputImageParam, error) {
	if err := validateImageContent(image); err != nil {
		return responses.ResponseInputImageParam{}, err
	}
	return responses.ResponseInputImageParam{
		Detail:   responses.ResponseInputImageDetailAuto,
		ImageURL: param.NewOpt(image.URL),
	}, nil
}

func openAIToolImageParam(image ImageContent) (responses.ResponseInputImageContentParam, error) {
	if err := validateImageContent(image); err != nil {
		return responses.ResponseInputImageContentParam{}, err
	}
	return responses.ResponseInputImageContentParam{
		Detail:   responses.ResponseInputImageContentDetailAuto,
		ImageURL: param.NewOpt(image.URL),
	}, nil
}

func openAIFileParam(file FileContent) (responses.ResponseInputFileParam, error) {
	if err := validateFileContent(file); err != nil {
		return responses.ResponseInputFileParam{}, err
	}
	kind, _ := contentSourceScheme(file.URL)
	input := responses.ResponseInputFileParam{
		Detail: responses.ResponseInputFileDetailAuto,
	}
	if kind == "data" {
		input.FileData = param.NewOpt(file.URL)
		input.Filename = param.NewOpt(file.Filename)
	} else {
		// OpenAI treats filename as mutually exclusive with URL-backed input.
		// The signed URL remains model-facing only; filename is required only
		// for inline file_data.
		input.FileURL = param.NewOpt(file.URL)
	}
	return input, nil
}

func openAIToolFileParam(file FileContent) (responses.ResponseInputFileContentParam, error) {
	if err := validateFileContent(file); err != nil {
		return responses.ResponseInputFileContentParam{}, err
	}
	kind, _ := contentSourceScheme(file.URL)
	input := responses.ResponseInputFileContentParam{
		Detail: responses.ResponseInputFileContentDetailAuto,
	}
	if kind == "data" {
		input.FileData = param.NewOpt(file.URL)
		input.Filename = param.NewOpt(file.Filename)
	} else {
		input.FileURL = param.NewOpt(file.URL)
	}
	return input, nil
}

func validateImageContent(image ImageContent) error {
	if err := validateImageMediaType(image.MediaType); err != nil {
		return err
	}
	return validateContentSource(image.URL, image.MediaType)
}

func validateFileContent(file FileContent) error {
	if err := validateFileMetadata(file.Filename, file.MediaType); err != nil {
		return err
	}
	return validateContentSource(file.URL, file.MediaType)
}

func contentSourceScheme(rawURL string) (string, error) {
	if err := validateContentURL(rawURL); err != nil {
		return "", err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return strings.ToLower(parsed.Scheme), nil
}

func isImageMediaType(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "image/")
}

func openAIContentError(role string, index int, contentType string, err error) error {
	return fmt.Errorf("openai: unsupported %s content at index %d (%s): %w", role, index, contentType, err)
}

func signedReasoningItem(signature string) (responses.ResponseReasoningItemParam, bool) {
	if signature == "" {
		return responses.ResponseReasoningItemParam{}, false
	}
	var item responses.ResponseReasoningItem
	if err := json.Unmarshal([]byte(signature), &item); err != nil || item.ID == "" || string(item.Type) != "reasoning" {
		return responses.ResponseReasoningItemParam{}, false
	}
	return item.ToParam(), true
}

func signedOutputMessage(signature string) (responses.ResponseOutputMessageParam, bool) {
	if signature == "" {
		return responses.ResponseOutputMessageParam{}, false
	}
	var item responses.ResponseOutputMessage
	if err := json.Unmarshal([]byte(signature), &item); err != nil || item.ID == "" || string(item.Type) != "message" {
		return responses.ResponseOutputMessageParam{}, false
	}
	return item.ToParam(), true
}

func toOpenAITools(tools []ToolSchema) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		definition := responses.FunctionToolParam{
			Name:       tool.Name,
			Parameters: tool.Parameters,
			Strict:     param.NewOpt(false),
		}
		if tool.Description != "" {
			definition.Description = param.NewOpt(tool.Description)
		}
		out = append(out, responses.ToolUnionParam{OfFunction: &definition})
	}
	return out
}

// textOfContent concatenates the text blocks of a content slice.
func textOfContent(content []Content) string {
	var text strings.Builder
	for _, block := range content {
		if value, ok := block.(TextContent); ok {
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(value.Text)
		}
	}
	return text.String()
}

func reasoningEffort(level string) shared.ReasoningEffort {
	switch level {
	case "minimal", "low", "medium", "high", "xhigh":
		return shared.ReasoningEffort(level)
	case "none", "off":
		return shared.ReasoningEffortNone
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
