package tool

import (
	"context"
	"encoding/json"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	storepkg "github.com/QuakeWang/ori/internal/store"
)

// Context provides runtime context to tool handlers.
type Context struct {
	SessionID string
	Workspace string
	State     *session.State
	Store     storepkg.Store
	Skills    []skill.Skill // pre-discovered catalog; nil means caller did not provide
}

// Result is the structured output of a tool execution.
type Result struct {
	Text      string         `json:"text"`
	Meta      map[string]any `json:"meta,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

// Handler is the function signature that all tool implementations must conform to.
// Input is the raw JSON arguments from the model's tool call.
type Handler func(ctx context.Context, tc *Context, input json.RawMessage) (*Result, error)

// Spec defines a tool's metadata and schema.
type Spec struct {
	Name        string          // external dotted name, e.g. "bash.output"
	Description string          // human-readable description
	Schema      json.RawMessage // JSON Schema for the tool's input
	Dangerous   bool            // requires explicit user consent
}

// Tool binds a Spec to its Handler.
type Tool struct {
	Spec    Spec
	Handler Handler
}

// Filter controls which tools are visible.
//
// Convention:
//   - AllowedTools == nil:           no restriction (default: non-dangerous only)
//   - AllowedTools == {} (non-nil):  deny all
//   - AllowedTools has entries:      whitelist
//   - IncludeAll:                    bypass all filtering (for `ori tools` listing)
type Filter struct {
	AllowedTools map[string]bool
	IncludeAll   bool
}
