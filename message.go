package droids

// message.go — the neutral conversation vocabulary shared by every layer.
// Providers translate to/from their wire format at the edge; the loop and
// storage only ever see these types.

// Role identifies who produced a message.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

// Content is a sealed interface over the block types a message can carry.
type Content interface{ isContent() }

// TextContent is plain text emitted by a user or assistant.
type TextContent struct {
	Text string
	// Signature is opaque provider metadata (e.g. OpenAI responses item id)
	// that must be replayed on subsequent turns. Empty when unused.
	Signature string
}

func (TextContent) isContent() {}

// ThinkingContent is reasoning/thinking output from a model.
type ThinkingContent struct {
	Thinking  string
	Signature string
	// Redacted marks thinking hidden by safety filters; Signature then holds
	// the opaque encrypted payload for multi-turn continuity.
	Redacted bool
}

func (ThinkingContent) isContent() {}

// ImageContent is a base64-encoded image block.
type ImageContent struct {
	Data     string // base64 encoded
	MimeType string // e.g. "image/png"
}

func (ImageContent) isContent() {}

// ToolCall is a model request to invoke a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments []byte // raw JSON arguments
}

func (ToolCall) isContent() {}

// Message is a sealed interface over the three conversation message kinds.
// It is the durable unit persisted by Storage.
type Message interface {
	isMessage() //nolint:unused // seals the interface
	Role() Role
}

// UserMessage is input from the user.
type UserMessage struct {
	Content   []Content
	Timestamp int64 // unix millis
}

func (UserMessage) isMessage() {}
func (UserMessage) Role() Role { return RoleUser }

// AssistantMessage is a full model response for one turn.
type AssistantMessage struct {
	Content       []Content // TextContent | ThinkingContent | ToolCall
	Provider      string
	Model         string
	ResponseModel string // concrete model when it differs from the requested one
	ResponseID    string
	Usage         Usage
	StopReason    StopReason
	ErrorMessage  string
	Timestamp     int64
}

func (AssistantMessage) isMessage() {}
func (AssistantMessage) Role() Role { return RoleAssistant }

// ToolCalls returns the tool-call blocks in this message, in order.
func (m AssistantMessage) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, c := range m.Content {
		if tc, ok := c.(ToolCall); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// ToolResultMessage is the result of executing a single tool call.
type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []Content // TextContent | ImageContent
	Details    any
	IsError    bool
	Timestamp  int64
}

func (ToolResultMessage) isMessage() {}
func (ToolResultMessage) Role() Role { return RoleToolResult }

// StopReason explains why an assistant turn ended.
type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)
