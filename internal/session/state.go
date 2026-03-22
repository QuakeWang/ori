package session

import "encoding/json"

// Activation represents a skill activation with optional overrides.
// This matches the design contract for Step 3/5 compatibility.
type Activation struct {
	Name             string
	MaxStepsOverride *int
	Metadata         map[string]json.RawMessage
}

// State holds per-session runtime state.
// Core-owned fields are explicit and typed.
//
// AllowedTools / AllowedSkills convention:
//   - nil:           no restriction (default)
//   - non-nil empty: deny all
//   - non-nil with entries: whitelist
type State struct {
	SessionID       string
	Workspace       string
	ActivatedSkills map[string]Activation
	AllowedTools    map[string]bool
	AllowedSkills   map[string]bool

	// Extras holds plugin-injected runtime state that is not part of
	// the core schema (e.g. budget, channel metadata, approval flags).
	// Inherited by sub-agents via shallow copy of each key.
	Extras map[string]json.RawMessage
}

// NewState creates a new State with initialized maps.
// AllowedTools and AllowedSkills are intentionally nil (= no restriction).
func NewState(sessionID, workspace string) *State {
	return &State{
		SessionID:       sessionID,
		Workspace:       workspace,
		ActivatedSkills: make(map[string]Activation),
		Extras:          make(map[string]json.RawMessage),
	}
}
