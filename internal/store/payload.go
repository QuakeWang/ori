package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/QuakeWang/ori/internal/llm"
)

// ------------------------------------------------------------------ payload types

// UserPayload is the payload for KindUser entries.
type UserPayload struct {
	Message llm.Message `json:"message"`
}

// AssistantPayload is the payload for KindAssistant entries.
type AssistantPayload struct {
	Message      llm.Message `json:"message"`
	FinishReason string      `json:"finish_reason,omitempty"`
	Usage        llm.Usage   `json:"usage,omitempty"`
}

// ToolCallPayload is the payload for KindToolCall entries.
type ToolCallPayload struct {
	Content string         `json:"content,omitempty"`
	Calls   []llm.ToolCall `json:"calls"`
}

// ToolResultItem represents a single tool result within a ToolResultPayload.
// ToolCallID is required to ensure tool call/result pairs are reconstructable.
type ToolResultItem struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    string `json:"content"`
}

// ToolResultPayload is the payload for KindToolResult entries.
type ToolResultPayload struct {
	Results []ToolResultItem `json:"results"`
}

// AnchorPayload is the payload for KindAnchor entries.
type AnchorPayload struct {
	Name    string         `json:"name"`
	Summary string         `json:"summary,omitempty"`
	State   map[string]any `json:"state,omitempty"`
}

// ------------------------------------------------------------------ entry constructors

// mustMarshal is used for typed payloads with known-safe field types.
// It panics only on truly impossible marshal failures (programming errors).
func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic("store: marshal payload: " + err.Error())
	}
	return data
}

// safeMarshal tries to marshal v and returns an error on failure instead of panicking.
func safeMarshal(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("store: marshal payload: %w", err)
	}
	return data, nil
}

// NewUserEntry creates a user entry from an LLM message.
func NewUserEntry(msg llm.Message) Entry {
	return Entry{
		Kind:    KindUser,
		Date:    time.Now(),
		Payload: mustMarshal(UserPayload{Message: msg}),
	}
}

// NewAssistantEntry creates an assistant entry.
func NewAssistantEntry(msg llm.Message, finishReason string, usage llm.Usage) Entry {
	return Entry{
		Kind: KindAssistant,
		Date: time.Now(),
		Payload: mustMarshal(AssistantPayload{
			Message:      msg,
			FinishReason: finishReason,
			Usage:        usage,
		}),
	}
}

// NewToolCallEntry creates a tool_call entry from a list of tool calls.
// Returns error instead of panicking because ToolCall.Arguments
// may contain non-JSON data from the model.
func NewToolCallEntry(calls []llm.ToolCall) (Entry, error) {
	return NewToolCallMessageEntry(llm.Message{Role: "assistant", ToolCalls: calls})
}

// NewToolCallMessageEntry creates a tool_call entry from an assistant message
// that may contain both text and tool calls.
func NewToolCallMessageEntry(msg llm.Message) (Entry, error) {
	payload, err := safeMarshal(ToolCallPayload{
		Content: msg.Content,
		Calls:   msg.ToolCalls,
	})
	if err != nil {
		return Entry{}, fmt.Errorf("store: marshal tool_call payload: %w", err)
	}
	return Entry{
		Kind:    KindToolCall,
		Date:    time.Now(),
		Payload: payload,
	}, nil
}

// NewToolResultEntry creates a tool_result entry.
// Every result item must have a non-empty ToolCallID to ensure
// reconstructability per GO_REWRITE_PLAN §6.
func NewToolResultEntry(results []ToolResultItem) (Entry, error) {
	for i, r := range results {
		if r.ToolCallID == "" {
			return Entry{}, fmt.Errorf("store: tool result item %d missing tool_call_id", i)
		}
	}
	return Entry{
		Kind:    KindToolResult,
		Date:    time.Now(),
		Payload: mustMarshal(ToolResultPayload{Results: results}),
	}, nil
}

// NewAnchorEntry creates an anchor entry.
// If state contains unserializable values, falls back to an anchor without state.
func NewAnchorEntry(name string, state map[string]any) Entry {
	payload, err := safeMarshal(AnchorPayload{Name: name, State: state})
	if err != nil {
		payload = mustMarshal(AnchorPayload{Name: name})
	}
	return Entry{
		Kind:    KindAnchor,
		Date:    time.Now(),
		Payload: payload,
	}
}

// NewEventEntry creates a generic event entry with an opaque payload.
func NewEventEntry(payload map[string]any) Entry {
	data, err := safeMarshal(payload)
	if err != nil {
		data = mustMarshal(map[string]any{"error": "unserializable payload"})
	}
	return Entry{
		Kind:    KindEvent,
		Date:    time.Now(),
		Payload: data,
	}
}

// ------------------------------------------------------------------ state persistence

// StatePayload is the payload for KindState entries.
// It captures the session runtime state that must survive across
// process restarts (e.g. activated skills, tool/skill policy).
//
// AllowedTools / AllowedSkills follow the nil/empty convention:
// nil (omitted in JSON) = no restriction; {} = deny all.
// The json tags intentionally omit "omitempty" so that empty maps
// are serialized as {} instead of being dropped.
type StatePayload struct {
	ActivatedSkills map[string]ActivatedSkillState `json:"activated_skills,omitempty"`
	AllowedTools    map[string]bool                `json:"allowed_tools"`
	AllowedSkills   map[string]bool                `json:"allowed_skills"`
	Extras          map[string]json.RawMessage     `json:"extras,omitempty"`
}

// ActivatedSkillState is the serializable form of session.Activation.
type ActivatedSkillState struct {
	Name             string                     `json:"name"`
	MaxStepsOverride *int                       `json:"max_steps_override,omitempty"`
	Metadata         map[string]json.RawMessage `json:"metadata,omitempty"`
}

// NewStateEntry creates a state snapshot entry.
func NewStateEntry(payload StatePayload) Entry {
	return Entry{
		Kind:    KindState,
		Date:    time.Now(),
		Payload: mustMarshal(payload),
	}
}

// LoadLatestState scans entries (most recent first) for the last KindState
// entry and returns its payload. Returns nil if no state entry exists.
func LoadLatestState(entries []Entry) *StatePayload {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != KindState {
			continue
		}
		var sp StatePayload
		if err := json.Unmarshal(entries[i].Payload, &sp); err != nil {
			continue
		}
		return &sp
	}
	return nil
}
