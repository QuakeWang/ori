package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/llm"
)

// ------------------------------------------------------------------ entry constructors

func TestNewUserEntry(t *testing.T) {
	msg := llm.Message{Role: "user", Content: "hello"}
	e := NewUserEntry(msg)
	assert.Equal(t, KindUser, e.Kind)
	assert.NotZero(t, e.Date)

	var p UserPayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	assert.Equal(t, "hello", p.Message.Content)
}

func TestNewAssistantEntry(t *testing.T) {
	msg := llm.Message{Role: "assistant", Content: "response"}
	usage := llm.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
	e := NewAssistantEntry(msg, "stop", usage)
	assert.Equal(t, KindAssistant, e.Kind)

	var p AssistantPayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	assert.Equal(t, "response", p.Message.Content)
	assert.Equal(t, "stop", p.FinishReason)
	assert.Equal(t, 30, p.Usage.TotalTokens)
}

func TestNewToolCallEntry(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
	}
	e, err := NewToolCallEntry(calls)
	require.NoError(t, err)
	assert.Equal(t, KindToolCall, e.Kind)

	var p ToolCallPayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	require.Len(t, p.Calls, 1)
	assert.Equal(t, "call-1", p.Calls[0].ID)
}

func TestNewToolCallMessageEntry_PreservesAssistantContent(t *testing.T) {
	msg := llm.Message{
		Role:    "assistant",
		Content: "Let me inspect that.",
		ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
		},
	}

	e, err := NewToolCallMessageEntry(msg)
	require.NoError(t, err)

	var p ToolCallPayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	assert.Equal(t, "Let me inspect that.", p.Content)
	require.Len(t, p.Calls, 1)
	assert.Equal(t, "call-1", p.Calls[0].ID)
}

func TestNewToolResultEntry(t *testing.T) {
	results := []ToolResultItem{
		{ToolCallID: "call-1", Name: "bash", Content: "file.txt"},
	}
	e, err := NewToolResultEntry(results)
	require.NoError(t, err)
	assert.Equal(t, KindToolResult, e.Kind)

	var p ToolResultPayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	require.Len(t, p.Results, 1)
	assert.Equal(t, "file.txt", p.Results[0].Content)
}

func TestNewAnchorEntry(t *testing.T) {
	e := NewAnchorEntry("checkpoint-1", map[string]any{"step": 3})
	assert.Equal(t, KindAnchor, e.Kind)

	var p AnchorPayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	assert.Equal(t, "checkpoint-1", p.Name)
	assert.Equal(t, float64(3), p.State["step"])
}

// helper for tests that always have valid ToolCallIDs.
func newToolResultHelper(t *testing.T, results []ToolResultItem) Entry {
	t.Helper()
	e, err := NewToolResultEntry(results)
	require.NoError(t, err)
	return e
}

func newToolCallHelper(t *testing.T, calls []llm.ToolCall) Entry {
	t.Helper()
	e, err := NewToolCallEntry(calls)
	require.NoError(t, err)
	return e
}

func newToolCallMessageHelper(t *testing.T, msg llm.Message) Entry {
	t.Helper()
	e, err := NewToolCallMessageEntry(msg)
	require.NoError(t, err)
	return e
}

func TestNewToolResultEntry_MissingToolCallID(t *testing.T) {
	_, err := NewToolResultEntry([]ToolResultItem{
		{ToolCallID: "", Name: "bash", Content: "out"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing tool_call_id")
}

// ------------------------------------------------------------------ JSONL store

func TestJSONLStore_AppendAndList(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	msg1 := llm.Message{Role: "user", Content: "first"}
	msg2 := llm.Message{Role: "user", Content: "second"}
	require.NoError(t, s.Append("sess1", NewUserEntry(msg1)))
	require.NoError(t, s.Append("sess1", NewUserEntry(msg2)))

	entries, err := s.List("sess1", 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// IDs should be monotonic.
	assert.Equal(t, int64(1), entries[0].ID)
	assert.Equal(t, int64(2), entries[1].ID)
}

func TestJSONLStore_ListWithLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "msg"})))
	}

	entries, err := s.List("sess1", 3)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	// Should return the 3 most recent.
	assert.Equal(t, int64(3), entries[0].ID)
	assert.Equal(t, int64(5), entries[2].ID)
}

func TestJSONLStore_IncrementalRead(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "first"})))
	entries1, err := s.List("sess1", 0)
	require.NoError(t, err)
	require.Len(t, entries1, 1)

	// Append more after first read.
	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "second"})))
	entries2, err := s.List("sess1", 0)
	require.NoError(t, err)
	require.Len(t, entries2, 2)
}

func TestJSONLStore_Reset(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "msg"})))
	require.NoError(t, s.Reset("sess1"))

	entries, err := s.List("sess1", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)

	// After reset, ID should restart from 1.
	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "new"})))
	entries, err = s.List("sess1", 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, int64(1), entries[0].ID)
}

func TestJSONLStore_AddAnchorAndInfo(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "msg"})))
	require.NoError(t, s.AddAnchor("sess1", "checkpoint", nil))
	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "after anchor"})))

	info, err := s.Info("sess1")
	require.NoError(t, err)
	assert.Equal(t, "sess1", info.SessionID)
	assert.Equal(t, 3, info.Entries)
	assert.Equal(t, 1, info.Anchors)
	assert.Equal(t, "checkpoint", info.LastAnchor)
	assert.Equal(t, 1, info.EntriesSinceLastAnchor)
}

func TestJSONLStore_EmptySession(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	entries, err := s.List("nonexistent", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)

	info, err := s.Info("nonexistent")
	require.NoError(t, err)
	assert.Equal(t, 0, info.Entries)
}

// ------------------------------------------------------------------ redaction

func TestRedactPayload_RemovesImageParts(t *testing.T) {
	payload := json.RawMessage(`{"message":{"Role":"user","Content":"hi","Parts":[{"Type":"text","Text":"hello"},{"Type":"image_url","ImageURL":{"URL":"data:..."}}]}}`)
	redacted := redactPayload(payload)

	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(redacted, &result))

	var msg map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(result["message"], &msg))

	var parts []map[string]any
	require.NoError(t, json.Unmarshal(msg["Parts"], &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "text", parts[0]["Type"])
}

func TestRedactPayload_NoPartsUnchanged(t *testing.T) {
	payload := json.RawMessage(`{"message":{"Role":"user","Content":"plain text"}}`)
	redacted := redactPayload(payload)
	assert.JSONEq(t, string(payload), string(redacted))
}

func TestRedactPayload_AllTextPartsUnchanged(t *testing.T) {
	payload := json.RawMessage(`{"message":{"Role":"user","Content":"hi","Parts":[{"Type":"text","Text":"a"},{"Type":"text","Text":"b"}]}}`)
	redacted := redactPayload(payload)
	assert.JSONEq(t, string(payload), string(redacted))
}

// ------------------------------------------------------------------ reducer

func TestDefaultReducer_AllKinds(t *testing.T) {
	entries := []Entry{
		NewUserEntry(llm.Message{Role: "user", Content: "hello"}),
		NewAssistantEntry(llm.Message{Role: "assistant", Content: "hi"}, "stop", llm.Usage{}),
		NewAnchorEntry("ckpt", nil),
		// Entries after the anchor should be included.
		NewUserEntry(llm.Message{Role: "user", Content: "after anchor"}),
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "bash", Content: "output"},
		}),
		NewEventEntry(map[string]any{"type": "some_event"}),
	}

	r := &DefaultReducer{}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	// Windowed: start from anchor. Anchor + user + tool_call + tool_result = 4.
	// Event entries are skipped. Entries before anchor are skipped.
	require.Len(t, msgs, 4)

	assert.Equal(t, "assistant", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "[Anchor: ckpt]")

	assert.Equal(t, "user", msgs[1].Role)
	assert.Equal(t, "after anchor", msgs[1].Content)

	assert.Equal(t, "assistant", msgs[2].Role)
	require.Len(t, msgs[2].ToolCalls, 1)
	assert.Equal(t, "c1", msgs[2].ToolCalls[0].ID)

	assert.Equal(t, "tool", msgs[3].Role)
	assert.Equal(t, "c1", msgs[3].ToolCallID)
	assert.Equal(t, "output", msgs[3].Content)
}

func TestDefaultReducer_Empty(t *testing.T) {
	r := &DefaultReducer{}
	msgs, err := r.Reduce(nil)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestDefaultReducer_ToolResultMultiple(t *testing.T) {
	entries := []Entry{
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "bash", Content: "out1"},
			{ToolCallID: "c2", Name: "fs.read", Content: "out2"},
		}),
	}

	r := &DefaultReducer{}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)
	// Each result item becomes a separate message.
	require.Len(t, msgs, 2)
	assert.Equal(t, "c1", msgs[0].ToolCallID)
	assert.Equal(t, "c2", msgs[1].ToolCallID)
}

func TestDefaultReducer_ToolCallPreservesAssistantContent(t *testing.T) {
	entries := []Entry{
		newToolCallMessageHelper(t, llm.Message{
			Role:    "assistant",
			Content: "Let me run ls first.",
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
			},
		}),
	}

	r := &DefaultReducer{}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "assistant", msgs[0].Role)
	assert.Equal(t, "Let me run ls first.", msgs[0].Content)
	require.Len(t, msgs[0].ToolCalls, 1)
	assert.Equal(t, "c1", msgs[0].ToolCalls[0].ID)
}

// ------------------------------------------------------------------ context compression (windowed truncation)

func TestDefaultReducer_WindowTruncatesOldToolResult(t *testing.T) {
	longContent := strings.Repeat("x", 500) // 500 chars, exceeds default 300

	entries := []Entry{
		// Old entries (outside window):
		NewUserEntry(llm.Message{Role: "user", Content: "q1"}),
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.sql", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.sql", Content: longContent},
		}),
		NewAssistantEntry(llm.Message{Role: "assistant", Content: "analysis"}, "stop", llm.Usage{}),
		// Recent entries (inside window):
		NewUserEntry(llm.Message{Role: "user", Content: "q2"}),
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c2", Name: "doris.sql", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c2", Name: "doris.sql", Content: longContent},
		}),
	}

	r := &DefaultReducer{WindowSize: 3} // last 3 entries are in window
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	// Find tool result messages.
	var toolResults []llm.Message
	for _, m := range msgs {
		if m.Role == "tool" {
			toolResults = append(toolResults, m)
		}
	}
	require.Len(t, toolResults, 2)

	// First tool_result (outside window) should be truncated.
	assert.Less(t, len(toolResults[0].Content), len(longContent))
	assert.Contains(t, toolResults[0].Content, "truncated")
	assert.Contains(t, toolResults[0].Content, "500 chars total")

	// Second tool_result (inside window) should be full.
	assert.Equal(t, longContent, toolResults[1].Content)
}

func TestDefaultReducer_WindowZeroDisablesCompression(t *testing.T) {
	longContent := strings.Repeat("y", 500)

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "bash", Content: longContent},
		}),
	}

	r := &DefaultReducer{WindowSize: 0} // disabled
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	// Tool result should be full (no truncation).
	toolMsg := msgs[len(msgs)-1]
	assert.Equal(t, longContent, toolMsg.Content)
}

func TestDefaultReducer_WindowZeroSkipsInWindowCap(t *testing.T) {
	// Regression: WindowSize=0 must also skip MaxToolResultInWindow,
	// otherwise huge results are silently truncated despite disablement.
	hugeContent := strings.Repeat("H", 50000) // well above the 8000 default cap

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.profile", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.profile", Content: hugeContent},
		}),
	}

	r := &DefaultReducer{WindowSize: 0} // disabled — no truncation at all
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			assert.Equal(t, hugeContent, m.Content,
				"WindowSize=0 should disable ALL truncation including in-window cap")
		}
	}
}

func TestDefaultReducer_AnchorPlusWindow(t *testing.T) {
	longContent := strings.Repeat("z", 500)

	entries := []Entry{
		// Before anchor — should be discarded entirely.
		NewUserEntry(llm.Message{Role: "user", Content: "old"}),
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c0", Name: "bash", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c0", Name: "bash", Content: longContent},
		}),
		// Anchor.
		NewAnchorEntry("ckpt", nil),
		// After anchor, outside window:
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.sql", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.sql", Content: longContent},
		}),
		// After anchor, inside window:
		NewUserEntry(llm.Message{Role: "user", Content: "recent"}),
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c2", Name: "doris.sql", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c2", Name: "doris.sql", Content: longContent},
		}),
	}

	r := &DefaultReducer{WindowSize: 3} // last 3 post-anchor entries in window
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	// Pre-anchor entries should not appear at all.
	for _, m := range msgs {
		assert.NotEqual(t, "old", m.Content)
		assert.NotEqual(t, "c0", m.ToolCallID)
	}

	// Find tool results.
	var toolResults []llm.Message
	for _, m := range msgs {
		if m.Role == "tool" {
			toolResults = append(toolResults, m)
		}
	}
	require.Len(t, toolResults, 2)

	// First (c1, outside window after anchor) should be truncated.
	assert.Contains(t, toolResults[0].Content, "truncated")

	// Second (c2, inside window) should be full.
	assert.Equal(t, longContent, toolResults[1].Content)
}

func TestDefaultReducer_ShortToolResultNotTruncated(t *testing.T) {
	shortContent := "ok" // well below any limit

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.ping", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.ping", Content: shortContent},
		}),
		// Padding to push tool_result outside window.
		NewUserEntry(llm.Message{Role: "user", Content: "pad1"}),
		NewUserEntry(llm.Message{Role: "user", Content: "pad2"}),
		NewUserEntry(llm.Message{Role: "user", Content: "pad3"}),
	}

	r := &DefaultReducer{WindowSize: 2, MaxToolResultLen: 50}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	// The short tool result, even outside window, should NOT be truncated.
	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			assert.Equal(t, shortContent, m.Content)
			assert.NotContains(t, m.Content, "truncated")
		}
	}
}

func TestDefaultReducer_TruncationFormat(t *testing.T) {
	content := strings.Repeat("A", 100)

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "bash", Content: content},
		}),
		// Push outside window.
		NewUserEntry(llm.Message{Role: "user", Content: "pad"}),
	}

	r := &DefaultReducer{WindowSize: 1, MaxToolResultLen: 20}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	var toolMsg llm.Message
	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			toolMsg = m
		}
	}

	// Should start with first 20 chars of original content.
	assert.True(t, strings.HasPrefix(toolMsg.Content, strings.Repeat("A", 20)))
	// Should end with truncation notice.
	assert.Contains(t, toolMsg.Content, "...(truncated, 100 chars total)")
}

func TestDefaultReducer_WindowIgnoresEventState(t *testing.T) {
	// Verify that event/state entries do NOT consume window quota.
	// Without this fix, the event and state entries would push the
	// tool_result outside the window even though they are invisible.
	longContent := strings.Repeat("v", 500)

	entries := []Entry{
		// Visible: tool_call + tool_result (these should be inside window).
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.sql", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.sql", Content: longContent},
		}),
		// Invisible entries that follow every tool step in real usage.
		NewEventEntry(map[string]any{"name": "run", "data": map[string]any{"usage": 100}}),
		NewEventEntry(map[string]any{"name": "tool_exec"}),
		// Visible: user message.
		NewUserEntry(llm.Message{Role: "user", Content: "next question"}),
	}

	// WindowSize=3 should cover all 3 visible entries (tool_call, tool_result, user).
	// The 2 event entries must NOT shrink the effective window.
	r := &DefaultReducer{WindowSize: 3}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	// tool_result should be full (inside window), not truncated.
	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			assert.Equal(t, longContent, m.Content,
				"tool_result should NOT be truncated when only event entries push it outside raw count")
		}
	}

	// Events should not appear in output at all.
	for _, m := range msgs {
		assert.NotContains(t, m.Content, "tool_exec")
	}
}

func TestDefaultReducer_TruncationUTF8Safe(t *testing.T) {
	// 10 Chinese characters = 10 runes, 30 bytes.
	content := "你好世界测试数据输出完"

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "bash", Content: content},
		}),
		// Push outside window.
		NewUserEntry(llm.Message{Role: "user", Content: "pad"}),
	}

	// maxLen=5 runes → should keep first 5 Chinese chars intact.
	r := &DefaultReducer{WindowSize: 1, MaxToolResultLen: 5}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	var toolMsg llm.Message
	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			toolMsg = m
		}
	}

	// Must start with exactly first 5 Chinese chars, not broken bytes.
	assert.True(t, strings.HasPrefix(toolMsg.Content, "你好世界测"))
	// Must report rune count (11), not byte count (33).
	assert.Contains(t, toolMsg.Content, "11 chars total")
	// Must not contain replacement characters (broken UTF-8).
	assert.NotContains(t, toolMsg.Content, "\uFFFD")
}

// ------------------------------------------------------------------ in-window truncation

func TestDefaultReducer_InWindowTruncatesLargeToolResult(t *testing.T) {
	// A tool_result inside the window that exceeds MaxToolResultInWindow
	// should be truncated to the in-window limit.
	hugeContent := strings.Repeat("P", 20000) // 20K chars, exceeds 8000 default

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.profile", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.profile", Content: hugeContent},
		}),
		NewUserEntry(llm.Message{Role: "user", Content: "analyze this"}),
	}

	// WindowSize=10 covers all 3 visible entries → all inside window.
	// MaxToolResultInWindow=500 should still cap the huge result.
	r := &DefaultReducer{WindowSize: 10, MaxToolResultInWindow: 500}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			assert.Less(t, len(m.Content), len(hugeContent))
			assert.Contains(t, m.Content, "truncated")
			assert.Contains(t, m.Content, "20000 chars total")
		}
	}
}

func TestDefaultReducer_InWindowSmallToolResultPreserved(t *testing.T) {
	// A tool_result inside the window that is below MaxToolResultInWindow
	// should be preserved in full.
	shortContent := "query completed in 150ms"

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.sql", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.sql", Content: shortContent},
		}),
		NewUserEntry(llm.Message{Role: "user", Content: "next"}),
	}

	r := &DefaultReducer{WindowSize: 10, MaxToolResultInWindow: 8000}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			assert.Equal(t, shortContent, m.Content)
		}
	}
}

func TestDefaultReducer_InWindowDisabledWithNegativeOne(t *testing.T) {
	// MaxToolResultInWindow = -1 disables in-window truncation entirely.
	hugeContent := strings.Repeat("X", 50000)

	entries := []Entry{
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.profile", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.profile", Content: hugeContent},
		}),
		NewUserEntry(llm.Message{Role: "user", Content: "pad"}),
	}

	r := &DefaultReducer{WindowSize: 10, MaxToolResultInWindow: -1}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	for _, m := range msgs {
		if m.ToolCallID == "c1" {
			assert.Equal(t, hugeContent, m.Content, "in-window truncation disabled, content should be full")
		}
	}
}

func TestDefaultReducer_AllEntriesInWindowStillTruncates(t *testing.T) {
	// This is the exact scenario that caused the original 1.7 MB context bug.
	// With only ~10 visible entries and WindowSize=30, ALL entries are inside
	// the window. Without MaxToolResultInWindow the 256 KB profile data would
	// pass through untruncated.
	profileData := strings.Repeat("Q", 100000) // simulate 100K profile

	entries := []Entry{
		NewUserEntry(llm.Message{Role: "user", Content: "analyze de0afeeab9734662-88bc0e8de4e4428c"}),
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c1", Name: "doris.sql", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c1", Name: "doris.sql", Content: "query_id found"},
		}),
		NewAssistantEntry(llm.Message{Role: "assistant", Content: "let me get profile"}, "stop", llm.Usage{}),
		newToolCallHelper(t, []llm.ToolCall{
			{ID: "c2", Name: "doris.profile", Arguments: json.RawMessage(`{}`)},
		}),
		newToolResultHelper(t, []ToolResultItem{
			{ToolCallID: "c2", Name: "doris.profile", Content: profileData},
		}),
	}

	// WindowSize=30|visibleCount=6 → all inside window.
	// Default MaxToolResultInWindow=8000 should cap the profile result.
	r := &DefaultReducer{WindowSize: 30}
	msgs, err := r.Reduce(entries)
	require.NoError(t, err)

	for _, m := range msgs {
		if m.ToolCallID == "c2" {
			assert.Less(t, len(m.Content), 9000,
				"in-window cap (default 8000) should truncate 100K profile")
			assert.Contains(t, m.Content, "truncated")
		}
		// Short tool result should be unaffected.
		if m.ToolCallID == "c1" {
			assert.Equal(t, "query_id found", m.Content)
		}
	}
}

func TestJSONLStore_RedactsOnWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	msg := llm.Message{
		Role:    "user",
		Content: "check this image",
		Parts: []llm.ContentPart{
			{Type: "text", Text: "check this image"},
			{Type: "image_url", ImageURL: &llm.ImageURLPart{URL: "data:image/png;base64,..."}},
		},
	}
	require.NoError(t, s.Append("sess1", NewUserEntry(msg)))

	entries, err := s.List("sess1", 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// The stored payload should have image parts redacted.
	var p UserPayload
	require.NoError(t, json.Unmarshal(entries[0].Payload, &p))
	for _, part := range p.Message.Parts {
		assert.NotEqual(t, "image_url", part.Type, "image_url parts should be redacted")
	}
}

// ------------------------------------------------------------------ path traversal prevention

func TestSafeFileName_DeterministicAndSafe(t *testing.T) {
	name := safeFileName("sess1")
	assert.Equal(t, 32, len(name), "hash should be 32 hex chars")
	// Same input → same output.
	assert.Equal(t, name, safeFileName("sess1"))
	// Different input → different output.
	assert.NotEqual(t, name, safeFileName("sess2"))
}

func TestSafeFileName_PathTraversalBlocked(t *testing.T) {
	// "../../../etc/passwd" should produce a normal hash filename, not escape.
	name := safeFileName("../../../etc/passwd")
	assert.Equal(t, 32, len(name))
	assert.NotContains(t, name, "..")
	assert.NotContains(t, name, "/")
}

func TestJSONLStore_DifferentSessionIDsIsolated(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	require.NoError(t, s.Append("sess-a", NewUserEntry(llm.Message{Role: "user", Content: "a"})))
	require.NoError(t, s.Append("sess-b", NewUserEntry(llm.Message{Role: "user", Content: "b"})))

	ea, _ := s.List("sess-a", 0)
	eb, _ := s.List("sess-b", 0)
	require.Len(t, ea, 1)
	require.Len(t, eb, 1)
	assert.Equal(t, "a", extractContent(t, ea[0]))
	assert.Equal(t, "b", extractContent(t, eb[0]))
}

func extractContent(t *testing.T, e Entry) string {
	t.Helper()
	var p UserPayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	return p.Message.Content
}

// ------------------------------------------------------------------ partial line recovery

func TestJSONLStore_CorruptMiddleLineSkipped(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	// Write one good entry via store.
	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "good"})))

	// Manually append a corrupt line WITH trailing newline (complete but bad).
	sf := s.getFile("sess1")
	f, err := os.OpenFile(sf.path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"id":2,"kind":"user","date":"2026-01-01T00:00:00Z","payload":{"broken` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Corrupt middle line is skipped (consumed), only 1 good entry returned.
	entries, err := s.List("sess1", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "good", extractContent(t, entries[0]))
}

func TestJSONLStore_PartialLastLineRetried(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "good"})))

	// Manually append a partial line WITHOUT trailing newline (in-progress write).
	sf := s.getFile("sess1")
	f, err := os.OpenFile(sf.path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"id":2,"kind":"user","date":"2026-01-01T00:00:00Z","payload":{"message":`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Partial last line is NOT consumed; only the 1 good entry is returned.
	entries, err := s.List("sess1", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	// If we "complete" the partial line by appending the rest + newline:
	f2, err := os.OpenFile(sf.path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f2.WriteString(`{"Role":"user","Content":"completed"}}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f2.Close())

	// Now re-read: the completed line should parse successfully.
	s2, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)
	entries2, err := s2.List("sess1", 0)
	require.NoError(t, err)
	assert.Len(t, entries2, 2)
}

func TestJSONLStore_AppendAfterCorruptedTailNoNewline(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	// Write one good entry.
	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "good"})))

	// Simulate a partial write without trailing newline (crash mid-write).
	sf := s.getFile("sess1")
	f, err := os.OpenFile(sf.path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	// Write partial JSON with NO newline at end.
	_, err = f.WriteString(`{"id":2,"kind":"user","date":"2026-01-01T00:00:00Z","payload":{"broken`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Now Append a new entry (the real scenario). The new entry should
	// land on its own line, separated from the corrupted tail.
	require.NoError(t, s.Append("sess1", NewUserEntry(llm.Message{Role: "user", Content: "after-crash"})))

	// Create a fresh store instance to read from disk without cache.
	s2, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)
	entries, err := s2.List("sess1", 0)
	require.NoError(t, err)

	// Should read: 1 good entry + 1 new entry. The corrupted middle line is skipped.
	require.Len(t, entries, 2, "should recover good entry and new entry, skipping corrupted line")
	assert.Equal(t, "good", extractContent(t, entries[0]))
	assert.Equal(t, "after-crash", extractContent(t, entries[1]))
}

// ------------------------------------------------------------------ AddAnchor error handling

func TestAddAnchor_UnserializableState_NoError(t *testing.T) {
	// NewAnchorEntry with unserializable state should not panic.
	// Instead it falls back to storing anchor without state.
	ch := make(chan int)
	assert.NotPanics(t, func() {
		e := NewAnchorEntry("safe", map[string]any{"bad": ch})
		assert.Equal(t, KindAnchor, e.Kind)
		var p AnchorPayload
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		assert.Equal(t, "safe", p.Name)
		// State should be nil (fallback stripped it).
		assert.Nil(t, p.State)
	})
}

func TestAddAnchor_ViaStore_UnserializableReturnsError(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	ch := make(chan int)
	err = s.AddAnchor("sess1", "bad-anchor", map[string]any{"bad": ch})
	assert.Error(t, err, "AddAnchor with unserializable state should return error")
}

// ------------------------------------------------------------------ append sequencing

func TestJSONLStore_Append_AssignsMonotonicIDs(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)

	entries := []Entry{
		NewUserEntry(llm.Message{Role: "user", Content: "batch-1"}),
		NewUserEntry(llm.Message{Role: "user", Content: "batch-2"}),
		NewUserEntry(llm.Message{Role: "user", Content: "batch-3"}),
	}
	for _, entry := range entries {
		require.NoError(t, s.Append("sess1", entry))
	}

	listed, err := s.List("sess1", 0)
	require.NoError(t, err)
	require.Len(t, listed, 3)
	assert.Equal(t, int64(1), listed[0].ID)
	assert.Equal(t, int64(3), listed[2].ID)
}

// ------------------------------------------------------------------ workspace isolation

func TestJSONLStore_WorkspaceIsolation(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewJSONLStore(dir, "workspace-a")
	require.NoError(t, err)
	s2, err := NewJSONLStore(dir, "workspace-b")
	require.NoError(t, err)

	// Same session ID in different workspaces.
	require.NoError(t, s1.Append("shared-sess", NewUserEntry(llm.Message{Role: "user", Content: "from-ws-a"})))
	require.NoError(t, s2.Append("shared-sess", NewUserEntry(llm.Message{Role: "user", Content: "from-ws-b"})))

	e1, _ := s1.List("shared-sess", 0)
	e2, _ := s2.List("shared-sess", 0)
	require.Len(t, e1, 1)
	require.Len(t, e2, 1)

	assert.Equal(t, "from-ws-a", extractContent(t, e1[0]))
	assert.Equal(t, "from-ws-b", extractContent(t, e2[0]))
}

func TestJSONLStore_SharedSessionConcurrentAppendsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewJSONLStore(dir, "workspace-shared")
	require.NoError(t, err)
	s2, err := NewJSONLStore(dir, "workspace-shared")
	require.NoError(t, err)

	const total = 40
	start := make(chan struct{})
	errCh := make(chan error, total)

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		idx := i
		target := s1
		if i%2 == 1 {
			target = s2
		}
		go func() {
			defer wg.Done()
			<-start
			errCh <- target.Append("shared-sess", NewUserEntry(llm.Message{
				Role:    "user",
				Content: fmt.Sprintf("msg-%02d", idx),
			}))
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	entries, err := s1.List("shared-sess", 0)
	require.NoError(t, err)
	require.Len(t, entries, total)

	seenIDs := make(map[int64]bool, total)
	for i, entry := range entries {
		assert.Equal(t, int64(i+1), entry.ID)
		assert.False(t, seenIDs[entry.ID], "entry ID %d should be unique", entry.ID)
		seenIDs[entry.ID] = true
	}
}

// ------------------------------------------------------------------ NewToolCallEntry error path

func TestNewToolCallEntry_InvalidArguments(t *testing.T) {
	// ToolCall.Arguments is json.RawMessage; if it contains
	// truly non-marshalable content via custom types, safeMarshal catches it.
	// With valid json.RawMessage (even malformed JSON string), json.Marshal
	// will succeed since RawMessage implements Marshaler.
	// This test verifies that the function completes without panic.
	calls := []llm.ToolCall{
		{ID: "c1", Name: "test", Arguments: json.RawMessage(`{"valid": true}`)},
	}
	e, err := NewToolCallEntry(calls)
	require.NoError(t, err)
	assert.Equal(t, KindToolCall, e.Kind)
}
