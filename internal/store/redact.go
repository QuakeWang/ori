package store

import "encoding/json"

// redactPayload removes non-text multimodal parts from a payload
// before persistence. Specifically, it strips image_url and other
// non-text content parts from user message payloads.
func redactPayload(payload json.RawMessage) json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return payload
	}

	// Check for "message" field (UserPayload/AssistantPayload).
	msgRaw, ok := raw["message"]
	if !ok {
		return payload
	}

	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return payload
	}

	// Redact "Parts" field if present (llm.Message has no JSON tags, so field is uppercase).
	partsRaw, ok := msg["Parts"]
	if !ok {
		return payload
	}

	var parts []map[string]any
	if err := json.Unmarshal(partsRaw, &parts); err != nil {
		return payload
	}

	// Keep only text parts. ContentPart has no JSON tags, so Type is uppercase.
	redacted := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if typ, _ := part["Type"].(string); typ == "text" {
			redacted = append(redacted, part)
		}
	}

	// If nothing was removed, return as-is.
	if len(redacted) == len(parts) {
		return payload
	}

	// Rebuild the payload with redacted parts.
	redactedParts, err := json.Marshal(redacted)
	if err != nil {
		return payload
	}
	msg["Parts"] = redactedParts

	newMsg, err := json.Marshal(msg)
	if err != nil {
		return payload
	}
	raw["message"] = newMsg

	result, err := json.Marshal(raw)
	if err != nil {
		return payload
	}
	return result
}
