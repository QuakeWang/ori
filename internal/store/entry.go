package store

import (
	"encoding/json"
	"time"
)

// Kind identifies the type of a store entry.
type Kind string

const (
	KindUser       Kind = "user"
	KindAssistant  Kind = "assistant"
	KindToolCall   Kind = "tool_call"
	KindToolResult Kind = "tool_result"
	KindAnchor     Kind = "anchor"
	KindSystem     Kind = "system"
	KindEvent      Kind = "event"
	KindState      Kind = "state"
)

// Entry is the fundamental unit of persisted session data.
// IDs are monotonic within a persisted session.
type Entry struct {
	ID      int64           `json:"id"`
	Kind    Kind            `json:"kind"`
	Date    time.Time       `json:"date"`
	Payload json.RawMessage `json:"payload"`
	Meta    map[string]any  `json:"meta,omitempty"`
}
