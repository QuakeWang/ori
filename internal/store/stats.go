package store

import "encoding/json"

func summarizeEntries(sessionID string, entries []Entry) SessionInfo {
	info := SessionInfo{
		SessionID: sessionID,
		Entries:   len(entries),
	}

	lastAnchorIdx := -1
	for i, e := range entries {
		if e.Kind == KindAnchor {
			info.Anchors++
			var ap AnchorPayload
			if err := json.Unmarshal(e.Payload, &ap); err == nil {
				info.LastAnchor = ap.Name
			}
			lastAnchorIdx = i
		}
		if e.Kind == KindAssistant {
			var ap AssistantPayload
			if err := json.Unmarshal(e.Payload, &ap); err == nil {
				info.LastTokenUsage = ap.Usage.TotalTokens
			}
		}
	}

	if lastAnchorIdx >= 0 {
		info.EntriesSinceLastAnchor = len(entries) - lastAnchorIdx - 1
	} else {
		info.EntriesSinceLastAnchor = len(entries)
	}

	return info
}
