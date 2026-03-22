package session

import storepkg "github.com/QuakeWang/ori/internal/store"

// Session represents an active agent session.
type Session struct {
	ID    string
	State *State
	Store storepkg.Store // per-session store; set by agent at creation
}

// New creates a new session with initialized state.
func New(id, workspace string) *Session {
	return &Session{
		ID:    id,
		State: NewState(id, workspace),
	}
}
