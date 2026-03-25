package store

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/QuakeWang/ori/internal/llm"
)

// Reducer converts stored entries back into model messages for context reconstruction.
type Reducer interface {
	Reduce(entries []Entry) ([]llm.Message, error)
}

// defaultMaxToolResultLen is a defensive fallback used when MaxToolResultLen
// is not explicitly set. Under normal operation the value is injected from
// config.DefaultContextMaxToolResult via bootstrap; keep the two constants in
// sync if either changes.
const defaultMaxToolResultLen = 300

// defaultMaxToolResultInWindow is the fallback for MaxToolResultInWindow.
// 8000 characters is enough to retain profile summaries, execution overviews,
// and key operator metrics while preventing a single tool result (e.g. 256 KB
// query profile) from inflating the LLM context.
const defaultMaxToolResultInWindow = 8000

// DefaultReducer implements windowed context reconstruction.
//
// Behavior:
//   - Finds the LAST anchor entry and discards everything before it.
//   - The anchor itself is rendered as an assistant message including its summary
//     (if present), so the model retains prior context awareness.
//   - user/assistant/tool_call/tool_result entries after the anchor are converted normally.
//   - event/system/state entries are always skipped.
//   - When WindowSize > 0, tool_result entries outside the most recent window
//     have their content truncated to MaxToolResultLen characters.
//   - Even inside the window, tool_result entries exceeding
//     MaxToolResultInWindow are truncated to prevent a single oversized
//     result (e.g. 256 KB query profile) from inflating the context.
type DefaultReducer struct {
	// WindowSize is the number of most recent visible entries (after anchor
	// truncation) that are preserved in full. Entries outside the window have
	// their tool_result content truncated.
	// Zero or negative means no windowing (all entries are full).
	WindowSize int

	// MaxToolResultLen is the maximum character length of a single tool_result
	// content when it falls outside the window. Longer content is truncated
	// with a suffix indicating the original length.
	// Zero means use the default (300 characters).
	MaxToolResultLen int

	// MaxToolResultInWindow is the maximum character length of a single
	// tool_result content when it falls inside the window. This provides a
	// generous upper bound that prevents a single oversized result from
	// inflating the overall context, while still retaining enough detail for
	// the model to reason effectively.
	// Zero means use the default (8000 characters).
	// Set to -1 to disable in-window truncation entirely.
	MaxToolResultInWindow int
}

// isVisibleKind returns true for entry kinds that produce LLM messages.
// event/system/state entries are skipped by the reducer and must not
// consume window quota.
//
// NOTE: update this function when adding new Kind constants in entry.go.
func isVisibleKind(k Kind) bool {
	switch k {
	case KindUser, KindAssistant, KindToolCall, KindToolResult, KindAnchor:
		return true
	default:
		return false
	}
}

// Reduce converts entries into LLM messages using a windowed approach.
func (r *DefaultReducer) Reduce(entries []Entry) ([]llm.Message, error) {
	lastAnchor := -1
	for i, e := range entries {
		if e.Kind == KindAnchor {
			lastAnchor = i
		}
	}

	var msgs []llm.Message

	start := 0
	if lastAnchor >= 0 {
		start = lastAnchor
	}

	visibleCount := 0
	for i := start; i < len(entries); i++ {
		if isVisibleKind(entries[i].Kind) {
			visibleCount++
		}
	}

	visibleWindowStart := 0
	maxLen := 0
	if r.WindowSize > 0 && visibleCount > r.WindowSize {
		visibleWindowStart = visibleCount - r.WindowSize
		maxLen = r.MaxToolResultLen
		if maxLen <= 0 {
			maxLen = defaultMaxToolResultLen
		}
	}

	// In-window cap: even entries inside the window are truncated when they
	// exceed MaxToolResultInWindow. This is a safety net against single
	// oversized results (e.g. 256 KB query profiles).
	// When WindowSize <= 0 (windowing disabled), skip the in-window cap
	// entirely so that "WindowSize=0 disables all truncation" holds true.
	inWindowMax := 0
	if r.WindowSize > 0 {
		inWindowMax = r.MaxToolResultInWindow
		if inWindowMax == 0 {
			inWindowMax = defaultMaxToolResultInWindow
		}
		if inWindowMax < 0 {
			inWindowMax = 0 // explicitly disabled
		}
	}

	visibleIdx := 0
	for i := start; i < len(entries); i++ {
		e := entries[i]
		if !isVisibleKind(e.Kind) {
			continue
		}
		inWindow := visibleIdx >= visibleWindowStart
		visibleIdx++

		switch e.Kind {
		case KindUser, KindAssistant:
			msg, err := reduceMessage(e)
			if err != nil {
				return nil, fmt.Errorf("reduce %s entry %d: %w", e.Kind, e.ID, err)
			}
			msgs = append(msgs, msg)

		case KindToolCall:
			msg, err := reduceToolCall(e)
			if err != nil {
				return nil, fmt.Errorf("reduce tool_call entry %d: %w", e.ID, err)
			}
			msgs = append(msgs, msg)

		case KindToolResult:
			var truncLen int
			if !inWindow {
				truncLen = maxLen
			} else if inWindowMax > 0 {
				truncLen = inWindowMax
			}
			results, err := reduceToolResult(e, truncLen)
			if err != nil {
				return nil, fmt.Errorf("reduce tool_result entry %d: %w", e.ID, err)
			}
			msgs = append(msgs, results...)

		case KindAnchor:
			msg := reduceAnchorWithSummary(e)
			msgs = append(msgs, msg)
		}
	}

	return msgs, nil
}

// messagePayload is the common shape shared by UserPayload and AssistantPayload.
// Both store exactly one llm.Message that the reducer needs to extract.
type messagePayload struct {
	Message llm.Message `json:"message"`
}

func reduceMessage(e Entry) (llm.Message, error) {
	var p messagePayload
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

// reduceToolResult converts a tool_result entry into LLM messages.
// When maxLen > 0, each result's content is truncated to that many characters.
// When maxLen <= 0, content is preserved in full.
func reduceToolResult(e Entry, maxLen int) ([]llm.Message, error) {
	var p ToolResultPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, err
	}
	msgs := make([]llm.Message, 0, len(p.Results))
	for _, r := range p.Results {
		content := r.Content
		if maxLen > 0 {
			content = truncateContent(content, maxLen)
		}
		msgs = append(msgs, llm.Message{
			Role:       "tool",
			Content:    content,
			ToolCallID: r.ToolCallID,
		})
	}
	return msgs, nil
}

// truncateContent shortens s to maxLen characters if it exceeds the limit.
// The truncated output includes a suffix indicating the original length.
// Uses a single pass: walks the string rune-by-rune, recording both the
// byte offset at maxLen and the total rune count, without allocating a
// []rune slice.
func truncateContent(s string, maxLen int) string {
	if maxLen <= 0 {
		return s
	}
	byteOffset := 0 // byte position of the (maxLen)th rune boundary
	offsetLocked := false
	runeCount := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		runeCount++
		if !offsetLocked {
			byteOffset = i + size
		}
		if runeCount == maxLen {
			offsetLocked = true
		}
		i += size
	}
	if runeCount <= maxLen {
		return s
	}
	return fmt.Sprintf("%s\n...(truncated, %d chars total)", s[:byteOffset], runeCount)
}

// reduceAnchorWithSummary renders an anchor as an assistant message
// that includes both the anchor name and its handoff summary (if any).
func reduceAnchorWithSummary(e Entry) llm.Message {
	var p AnchorPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return llm.Message{Role: "assistant", Content: "[Anchor]"}
	}

	content := fmt.Sprintf("[Anchor: %s]", p.Name)

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
