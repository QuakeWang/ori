package skill

import (
	"encoding/json"

	"github.com/QuakeWang/ori/internal/session"
)

const activeBlockedSQLExtraKey = "doris.active_blocked_sql"

// ApplyActivationRuntimeState stores extension-owned runtime state derived from
// an explicit skill activation. Doris blocked_sql follows dod semantics: the
// latest explicit skill activation replaces the previous blocked policy.
func ApplyActivationRuntimeState(state *session.State, act session.Activation) {
	if state == nil {
		return
	}

	raw, ok := act.Metadata["blocked_sql"]
	if !ok || len(raw) == 0 {
		delete(state.Extras, activeBlockedSQLExtraKey)
		return
	}

	if state.Extras == nil {
		state.Extras = make(map[string]json.RawMessage)
	}
	state.Extras[activeBlockedSQLExtraKey] = cloneRawMessage(raw)
}

// ActiveBlockedSQL returns the current blocked_sql payload selected by the
// latest explicit skill activation.
func ActiveBlockedSQL(state *session.State) json.RawMessage {
	if state == nil || state.Extras == nil {
		return nil
	}

	raw, ok := state.Extras[activeBlockedSQLExtraKey]
	if !ok || len(raw) == 0 {
		return nil
	}
	return cloneRawMessage(raw)
}

// BackfillActivationRuntimeState restores the runtime blocked_sql slot from
// legacy persisted activations when the session has exactly one unambiguous
// blocked_sql source and no newer runtime override has been stored yet.
func BackfillActivationRuntimeState(state *session.State) {
	if state == nil || len(ActiveBlockedSQL(state)) != 0 {
		return
	}

	var candidate json.RawMessage
	count := 0
	for _, act := range state.ActivatedSkills {
		raw, ok := act.Metadata["blocked_sql"]
		if !ok || len(raw) == 0 {
			continue
		}
		candidate = cloneRawMessage(raw)
		count++
		if count > 1 {
			return
		}
	}

	if count == 0 {
		return
	}
	if state.Extras == nil {
		state.Extras = make(map[string]json.RawMessage)
	}
	state.Extras[activeBlockedSQLExtraKey] = candidate
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}

	cloned := make(json.RawMessage, len(raw))
	copy(cloned, raw)
	return cloned
}
