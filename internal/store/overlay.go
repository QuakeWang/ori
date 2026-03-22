package store

import (
	"sync"
	"time"
)

// OverlayStore wraps a base Store and accumulates writes in memory.
// Changes can be committed to the base store via Merge, or silently
// discarded when the overlay is abandoned. This enables ephemeral
// sessions and trial execution without polluting the permanent tape.
//
// Invariants:
//   - Append/AddAnchor write to overlay only.
//   - List merges base + overlay.
//   - Reset marks the session as "base-hidden"; subsequent List calls
//     only return overlay entries, ignoring base history.
type OverlayStore struct {
	base     Store
	mu       sync.Mutex
	pending  map[string][]Entry // sessionID → appended entries
	nextID   map[string]int64
	hideBase map[string]bool // true  → base history is hidden after Reset
}

// NewOverlayStore creates an overlay on top of an existing store.
func NewOverlayStore(base Store) *OverlayStore {
	return &OverlayStore{
		base:     base,
		pending:  make(map[string][]Entry),
		nextID:   make(map[string]int64),
		hideBase: make(map[string]bool),
	}
}

// Append writes to the in-memory overlay only.
func (o *OverlayStore) Append(sessionID string, entry Entry) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	id := o.nextID[sessionID] + 1
	o.nextID[sessionID] = id
	entry.ID = id
	o.pending[sessionID] = append(o.pending[sessionID], entry)
	return nil
}

// List returns base entries merged with overlay entries.
// If the session has been Reset, base entries are excluded.
func (o *OverlayStore) List(sessionID string, limit int) ([]Entry, error) {
	o.mu.Lock()
	hidden := o.hideBase[sessionID]
	overlay := make([]Entry, len(o.pending[sessionID]))
	copy(overlay, o.pending[sessionID])
	o.mu.Unlock()

	var base []Entry
	if !hidden {
		var err error
		base, err = o.base.List(sessionID, 0)
		if err != nil {
			return nil, err
		}
	}

	// Re-number overlay entries to continue from base.
	baseMax := int64(0)
	if len(base) > 0 {
		baseMax = base[len(base)-1].ID
	}
	for i := range overlay {
		overlay[i].ID = baseMax + int64(i) + 1
	}

	all := append(base, overlay...)
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

// Reset marks the session as "base-hidden" and clears overlay entries.
// After Reset, List only returns new overlay entries,
// effectively isolating the ephemeral session from base history.
func (o *OverlayStore) Reset(sessionID string) error {
	o.mu.Lock()
	delete(o.pending, sessionID)
	delete(o.nextID, sessionID)
	o.hideBase[sessionID] = true
	o.mu.Unlock()
	return nil
}

// AddAnchor adds an anchor to the overlay.
func (o *OverlayStore) AddAnchor(sessionID, name string, state map[string]any) error {
	return o.AddAnchorWithSummary(sessionID, name, "", state)
}

// AddAnchorWithSummary adds an anchor with summary to the overlay.
func (o *OverlayStore) AddAnchorWithSummary(sessionID, name, summary string, state map[string]any) error {
	payload, err := safeMarshal(AnchorPayload{Name: name, Summary: summary, State: state})
	if err != nil {
		return err
	}
	return o.Append(sessionID, Entry{Kind: KindAnchor, Date: time.Now(), Payload: payload})
}

// Info returns combined info from base + overlay.
func (o *OverlayStore) Info(sessionID string) (SessionInfo, error) {
	entries, err := o.List(sessionID, 0)
	if err != nil {
		return SessionInfo{}, err
	}
	return summarizeEntries(sessionID, entries), nil
}

// Discard drops all overlay entries for a session without writing to base.
func (o *OverlayStore) Discard(sessionID string) {
	o.mu.Lock()
	delete(o.pending, sessionID)
	delete(o.nextID, sessionID)
	delete(o.hideBase, sessionID)
	o.mu.Unlock()
}
