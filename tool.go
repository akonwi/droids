package droids

import (
	"context"
	"encoding/json"
)

// tool.go — typed tools. Authors write a generic Tool[Args] with a strongly
// typed Execute; NewTool erases it into an AnyTool for the droid's tool list.
// The loop validates/decodes raw model arguments into Args before calling.

// ToolResult is what a tool returns to the model.
type ToolResult struct {
	// Content is returned to the model (text and/or images).
	Content []Content
	// Details is arbitrary structured data for logs/UI, persisted with the
	// tool result but not sent to the model.
	Details any
}

// ToolText is a convenience for a text-only tool result.
func ToolText(text string) ToolResult {
	return ToolResult{Content: []Content{TextContent{Text: text}}}
}

// Tool is a typed tool definition.
type Tool[Args any] struct {
	Name        string
	Description string
	// Parameters is the JSON Schema for Args. If nil, a minimal object schema
	// is used (schema derivation from Args is a future enhancement).
	Parameters map[string]any
	// Execute runs the tool with decoded arguments. Return an error to signal
	// failure; the loop converts it into an error tool result.
	Execute func(ctx context.Context, args Args) (ToolResult, error)
	// Mode overrides execution mode for this tool ("sequential" | "parallel").
	Mode ExecutionMode
}

// ExecutionMode controls whether a tool batch runs sequentially or in parallel.
type ExecutionMode string

const (
	ModeDefault    ExecutionMode = ""
	ModeSequential ExecutionMode = "sequential"
	ModeParallel   ExecutionMode = "parallel"
)

// AnyTool is the type-erased tool the runtime works with.
type AnyTool interface {
	schema() ToolSchema
	mode() ExecutionMode
	// execute decodes raw JSON args and runs the tool.
	execute(ctx context.Context, raw []byte) (ToolResult, error)
}

// NewTool erases a typed Tool[Args] into an AnyTool.
func NewTool[Args any](t Tool[Args]) AnyTool { return boundTool[Args]{t} }

type boundTool[Args any] struct{ t Tool[Args] }

func (b boundTool[Args]) schema() ToolSchema {
	params := b.t.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return ToolSchema{
		Name:        b.t.Name,
		Description: b.t.Description,
		Parameters:  params,
	}
}

func (b boundTool[Args]) mode() ExecutionMode { return b.t.Mode }

func (b boundTool[Args]) execute(ctx context.Context, raw []byte) (ToolResult, error) {
	var args Args
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return ToolResult{}, err
		}
	}
	return b.t.Execute(ctx, args)
}
