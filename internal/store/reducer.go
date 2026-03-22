package store

import (
	"encoding/json"
	"fmt"

	"github.com/QuakeWang/ori/internal/llm"
)

// Reducer converts stored entries back into model messages for context reconstruction.
type Reducer interface {
	Reduce(entries []Entry) ([]llm.Message, error)
}

// DefaultReducer implements windowed context reconstruction.
//
// Behavior:
//   - Finds the LAST anchor entry and discards everything before it.
//   - The anchor itself is rendered as an assistant message including its summary
//     (if present), so the model retains prior context awareness.
//   - user/assistant/tool_call/tool_result entries after the anchor are converted normally.
//   - event/system/state entries are always skipped.
type DefaultReducer struct{}

// Reduce converts entries into LLM messages using a windowed approach.
func (r *DefaultReducer) Reduce(entries []Entry) ([]llm.Message, error) {
	// Find last anchor index.
	lastAnchor := -1
	for i, e := range entries {
		if e.Kind == KindAnchor {
			lastAnchor = i
		}
	}

	var msgs []llm.Message

	// If an anchor exists, start from it (inclusive).
	start := 0
	if lastAnchor >= 0 {
		start = lastAnchor
	}

	for i := start; i < len(entries); i++ {
		e := entries[i]
		switch e.Kind {
		case KindUser:
			msg, err := reduceUser(e)
			if err != nil {
				return nil, fmt.Errorf("reduce user entry %d: %w", e.ID, err)
			}
			msgs = append(msgs, msg)

		case KindAssistant:
			msg, err := reduceAssistant(e)
			if err != nil {
				return nil, fmt.Errorf("reduce assistant entry %d: %w", e.ID, err)
			}
			msgs = append(msgs, msg)

		case KindToolCall:
			msg, err := reduceToolCall(e)
			if err != nil {
				return nil, fmt.Errorf("reduce tool_call entry %d: %w", e.ID, err)
			}
			msgs = append(msgs, msg)

		case KindToolResult:
			results, err := reduceToolResult(e)
			if err != nil {
				return nil, fmt.Errorf("reduce tool_result entry %d: %w", e.ID, err)
			}
			msgs = append(msgs, results...)

		case KindAnchor:
			msg := reduceAnchorWithSummary(e)
			msgs = append(msgs, msg)

		case KindEvent, KindSystem, KindState:
			continue
		}
	}

	return msgs, nil
}

func reduceUser(e Entry) (llm.Message, error) {
	var p UserPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return llm.Message{}, err
	}
	return p.Message, nil
}

func reduceAssistant(e Entry) (llm.Message, error) {
	var p AssistantPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return llm.Message{}, err
	}
	return p.Message, nil
}

func reduceToolCall(e Entry) (llm.Message, error) {
	var p ToolCallPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return llm.Message{}, err
	}
	return llm.Message{
		Role:      "assistant",
		Content:   p.Content,
		ToolCalls: p.Calls,
	}, nil
}

func reduceToolResult(e Entry) ([]llm.Message, error) {
	var p ToolResultPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, err
	}
	msgs := make([]llm.Message, 0, len(p.Results))
	for _, r := range p.Results {
		msgs = append(msgs, llm.Message{
			Role:       "tool",
			Content:    r.Content,
			ToolCallID: r.ToolCallID,
		})
	}
	return msgs, nil
}

// reduceAnchorWithSummary renders an anchor as an assistant message
// that includes both the anchor name and its handoff summary (if any).
func reduceAnchorWithSummary(e Entry) llm.Message {
	var p AnchorPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return llm.Message{Role: "assistant", Content: "[Anchor]"}
	}

	content := fmt.Sprintf("[Anchor: %s]", p.Name)

	// Include summary from the explicit field.
	if p.Summary != "" {
		content += "\n" + p.Summary
	}
	// Fallback: check legacy state["summary"] for backward compatibility.
	if p.Summary == "" && p.State != nil {
		if s, ok := p.State["summary"].(string); ok && s != "" {
			content += "\n" + s
		}
	}

	return llm.Message{Role: "assistant", Content: content}
}
