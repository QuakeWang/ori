package store

// SessionInfo provides summary statistics for a session.
type SessionInfo struct {
	SessionID              string
	Entries                int
	Anchors                int
	LastAnchor             string
	EntriesSinceLastAnchor int
	LastTokenUsage         int
}

// Store defines the contract for session event persistence.
type Store interface {
	Append(sessionID string, entry Entry) error
	List(sessionID string, limit int) ([]Entry, error)
	Reset(sessionID string) error
	AddAnchor(sessionID, name string, state map[string]any) error
	AddAnchorWithSummary(sessionID, name, summary string, state map[string]any) error
	Info(sessionID string) (SessionInfo, error)
}
