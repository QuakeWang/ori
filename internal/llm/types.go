package llm

import (
	"context"
	"encoding/json"
	"time"
)

// --------------- input abstraction ---------------

// Input represents inbound user input, supporting both plain text and
// multimodal content parts (e.g. text + image_url).
type Input struct {
	Text  string
	Parts []ContentPart
}

// IsMultimodal returns true if the input contains content parts.
func (i Input) IsMultimodal() bool { return len(i.Parts) > 0 }

// ContentPart represents one part of a multimodal input.
type ContentPart struct {
	Type     string        // "text" or "image_url"
	Text     string        // populated when Type == "text"
	ImageURL *ImageURLPart // populated when Type == "image_url"
}

// ImageURLPart carries an image URL for vision models.
type ImageURLPart struct {
	URL    string
	Detail string // "auto", "low", "high"; empty defaults to "auto"
}

// --------------- conversation messages ---------------

// Message represents a single message in a conversation context.
// For user/assistant messages, either Content or Parts is populated (not both).
// For tool result messages, Content carries the tool output text.
type Message struct {
	Role       string
	Content    string
	Parts      []ContentPart
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// --------------- tool schema ---------------

// ToolSchema describes a tool available to the model.
// Name and Description are human-readable; Schema is the JSON Schema
// for the tool's parameters.
type ToolSchema struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// --------------- request / response ---------------

// Request is the input to a chat completion call.
// System is injected as a system message by the client implementation.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSchema
	MaxTokens int
	Timeout   time.Duration
}

// Usage reports token consumption for a completion.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Response is the flattened result of a chat completion call.
// The client implementation is responsible for extracting the first choice.
type Response struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}

// --------------- convenience ---------------

// IsToolCall returns true if the response contains tool calls.
func (r *Response) IsToolCall() bool { return len(r.ToolCalls) > 0 }

// Client is the interface for LLM chat completion.
// Implementations must convert between these types and provider-specific SDKs.
type Client interface {
	Chat(ctx context.Context, req Request) (*Response, error)
}
