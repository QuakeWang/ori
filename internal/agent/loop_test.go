package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/config"
	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

// ------------------------------------------------------------------ mock LLM client

type mockLLM struct {
	responses []*llm.Response
	calls     int
}

func (m *mockLLM) Chat(_ context.Context, _ llm.Request) (*llm.Response, error) {
	if m.calls >= len(m.responses) {
		return &llm.Response{Text: "fallback", FinishReason: "stop"}, nil
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

// ------------------------------------------------------------------ helpers

func newTestAgent(t *testing.T, llmClient llm.Client) (*Agent, *store.JSONLStore) {
	t.Helper()
	return newTestAgentWithWorkspace(t, llmClient, t.TempDir())
}

func newTestAgentWithWorkspace(t *testing.T, llmClient llm.Client, workspace string) (*Agent, *store.JSONLStore) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Settings{
		Model:     "test-model",
		MaxSteps:  5,
		MaxTokens: 1024,
	}

	registry := tool.NewRegistry()
	// Register a simple echo tool for testing.
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{
			Name:        "echo",
			Description: "echoes input",
			Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		},
		Handler: func(_ context.Context, _ *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var args struct{ Text string }
			_ = json.Unmarshal(input, &args)
			return &tool.Result{Text: "echo: " + args.Text}, nil
		},
	}))

	skillSvc := skill.NewServiceWithSources()
	st, err := store.NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)
	reducer := &store.DefaultReducer{}

	agent := New(cfg, llmClient, registry, skillSvc, st, reducer, workspace, nil)
	return agent, st
}

// ------------------------------------------------------------------ loop tests

func TestLoop_SingleStepTextResponse(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Text: "Hello, world!", FinishReason: "stop"},
	}}
	agent, _ := newTestAgent(t, mock)

	result, err := agent.Run(context.Background(), "test-sess", llm.Input{Text: "hi"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "Hello, world!", result.Output)
	assert.Equal(t, 1, result.Steps)
	assert.Equal(t, "test-sess", result.SessionID)
}

func TestLoop_ToolCallThenText(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		// Step 1: model requests tool call.
		{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"text":"ping"}`)},
			},
		},
		// Step 2: model returns text after tool result.
		{Text: "Got echo result.", FinishReason: "stop"},
	}}
	agent, _ := newTestAgent(t, mock)

	result, err := agent.Run(context.Background(), "test-sess", llm.Input{Text: "call echo"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "Got echo result.", result.Output)
	assert.Equal(t, 2, result.Steps)
	assert.Equal(t, 2, mock.calls, "LLM should have been called twice")
}

func TestLoop_HintedSkillsAreNotPersisted(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".agents", "skills", "health-check")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: health-check\ndescription: Health\n---\nBody"),
		0o644,
	))

	mock := &mockLLM{responses: []*llm.Response{
		{Text: "done", FinishReason: "stop"},
	}}
	agent, st := newTestAgentWithWorkspace(t, mock, workspace)
	sess := agent.NewSession("hint-sess", RunOptions{})

	result, err := agent.RunTurn(context.Background(), sess, llm.Input{Text: "please use $health-check"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Output)
	assert.Empty(t, sess.State.ActivatedSkills)

	entries, err := st.List("hint-sess", 0)
	require.NoError(t, err)
	sp := store.LoadLatestState(entries)
	require.NotNil(t, sp)
	assert.Empty(t, sp.ActivatedSkills)
}

func TestLoop_MaxStepsReached(t *testing.T) {
	// All responses request tool calls, no text response.
	mock := &mockLLM{responses: make([]*llm.Response, 10)}
	for i := range mock.responses {
		mock.responses[i] = &llm.Response{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{
				{ID: "c" + string(rune('0'+i)), Name: "echo", Arguments: json.RawMessage(`{"text":"loop"}`)},
			},
		}
	}

	agent, _ := newTestAgent(t, mock)

	result, err := agent.Run(context.Background(), "test-sess", llm.Input{Text: "loop"}, RunOptions{})
	require.ErrorIs(t, err, ErrMaxStepsReached)
	require.NotNil(t, result)
	assert.Equal(t, 5, result.Steps, "should hit MaxSteps=5")
}

func TestLoop_ContextCancellation(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Text: "should not reach", FinishReason: "stop"},
	}}
	agent, _ := newTestAgent(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := agent.Run(ctx, "test-sess", llm.Input{Text: "hi"}, RunOptions{})
	assert.Error(t, err)
}

func TestLoop_PersistsEntries(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Text: "response text", FinishReason: "stop"},
	}}
	agent, st := newTestAgent(t, mock)

	_, err := agent.Run(context.Background(), "test-sess", llm.Input{Text: "query"}, RunOptions{})
	require.NoError(t, err)

	// anchor + user + assistant + event(loop.step) + state = 5 entries.
	entries, err := st.List("test-sess", 0)
	require.NoError(t, err)
	require.Len(t, entries, 5, "should have anchor + user + assistant + event + state entries")
	assert.Equal(t, store.KindAnchor, entries[0].Kind)
	assert.Equal(t, store.KindUser, entries[1].Kind)
	assert.Equal(t, store.KindAssistant, entries[2].Kind)
	assert.Equal(t, store.KindEvent, entries[3].Kind)
	assert.Equal(t, store.KindState, entries[4].Kind)
}

func TestRunTurn_FallsBackToAgentStore_WhenSessionStoreIsNil(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Text: "response text", FinishReason: "stop"},
	}}
	agent, st := newTestAgent(t, mock)

	sess := session.New("embedded-sess", agent.workspace)
	result, err := agent.RunTurn(context.Background(), sess, llm.Input{Text: "query"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "response text", result.Output)

	entries, err := st.List("embedded-sess", 0)
	require.NoError(t, err)
	require.Len(t, entries, 5, "fallback store should persist the full turn")
	assert.Equal(t, store.KindAnchor, entries[0].Kind)
	assert.Equal(t, store.KindUser, entries[1].Kind)
	assert.Equal(t, store.KindAssistant, entries[2].Kind)
	assert.Equal(t, store.KindEvent, entries[3].Kind)
	assert.Equal(t, store.KindState, entries[4].Kind)
}

func TestLoop_ToolCallPersistsEntries(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"text":"test"}`)},
			},
		},
		{Text: "done", FinishReason: "stop"},
	}}
	agent, st := newTestAgent(t, mock)

	_, err := agent.Run(context.Background(), "test-sess", llm.Input{Text: "test"}, RunOptions{})
	require.NoError(t, err)

	// anchor + user + tool_call + tool_result + event(step1) + assistant + event(step2) + state = 8.
	// Note: continuePrompt is NO LONGER persisted (ephemeral injection only).
	entries, err := st.List("test-sess", 0)
	require.NoError(t, err)
	require.Len(t, entries, 8)
	assert.Equal(t, store.KindAnchor, entries[0].Kind)
	assert.Equal(t, store.KindUser, entries[1].Kind)
	assert.Equal(t, store.KindToolCall, entries[2].Kind)
	assert.Equal(t, store.KindToolResult, entries[3].Kind)
	assert.Equal(t, store.KindEvent, entries[4].Kind) // loop.step continue
	assert.Equal(t, store.KindAssistant, entries[5].Kind)
	assert.Equal(t, store.KindEvent, entries[6].Kind) // loop.step ok
	assert.Equal(t, store.KindState, entries[7].Kind)
}

func TestLoop_MixedAssistantToolCallTextRoundTripsToNextRequest(t *testing.T) {
	capturedSecondTurn := false
	client := &captureLLM{
		onChat: func(_ context.Context, req llm.Request) (*llm.Response, error) {
			if !capturedSecondTurn {
				capturedSecondTurn = true
				return &llm.Response{
					Text:         "Let me inspect that first.",
					FinishReason: "tool_calls",
					ToolCalls: []llm.ToolCall{
						{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"text":"ping"}`)},
					},
				}, nil
			}

			found := false
			for _, msg := range req.Messages {
				if msg.Role == "assistant" &&
					msg.Content == "Let me inspect that first." &&
					len(msg.ToolCalls) == 1 &&
					msg.ToolCalls[0].ID == "call-1" {
					found = true
					break
				}
			}
			assert.True(t, found, "second LLM turn must receive assistant text together with tool calls")

			return &llm.Response{Text: "done", FinishReason: "stop"}, nil
		},
	}

	agent, _ := newTestAgent(t, client)
	result, err := agent.Run(context.Background(), "test-sess", llm.Input{Text: "test"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Output)
}

// ------------------------------------------------------------------ regression: workspace propagation

func TestLoop_WorkspacePassedToToolContext(t *testing.T) {
	// Register a tool that captures the workspace from its context.
	var capturedWorkspace string
	ws := t.TempDir()

	mock := &mockLLM{responses: []*llm.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			},
		},
		{Text: "done", FinishReason: "stop"},
	}}

	dir := t.TempDir()
	cfg := &config.Settings{Model: "m", MaxSteps: 5, MaxTokens: 1024}
	registry := tool.NewRegistry()
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{Name: "probe", Description: "captures workspace"},
		Handler: func(_ context.Context, tc *tool.Context, _ json.RawMessage) (*tool.Result, error) {
			capturedWorkspace = tc.Workspace
			return &tool.Result{Text: "ok"}, nil
		},
	}))
	skillSvc := skill.NewServiceWithSources()
	st, _ := store.NewJSONLStore(dir, "w")
	reducer := &store.DefaultReducer{}

	ag := New(cfg, mock, registry, skillSvc, st, reducer, ws, nil)
	_, err := ag.Run(context.Background(), "s1", llm.Input{Text: "go"}, RunOptions{})
	require.NoError(t, err)

	assert.Equal(t, ws, capturedWorkspace, "tool context must receive the agent workspace, not \".\"")
}

// ------------------------------------------------------------------ regression: AGENTS.md via workspace

func TestLoop_AgentsMDInjectedFromWorkspace(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("CUSTOM_RULE_XYZ"), 0o644))

	// Use a mock that captures the system prompt from the request.
	var capturedSystem string
	mock := &captureLLM{
		onChat: func(_ context.Context, req llm.Request) (*llm.Response, error) {
			capturedSystem = req.System
			return &llm.Response{Text: "ok", FinishReason: "stop"}, nil
		},
	}

	dir := t.TempDir()
	cfg := &config.Settings{Model: "m", MaxSteps: 5, MaxTokens: 1024}
	registry := tool.NewRegistry()
	skillSvc := skill.NewServiceWithSources()
	st, _ := store.NewJSONLStore(dir, "w")
	reducer := &store.DefaultReducer{}

	ag := New(cfg, mock, registry, skillSvc, st, reducer, ws, nil)
	_, err := ag.Run(context.Background(), "s1", llm.Input{Text: "hi"}, RunOptions{})
	require.NoError(t, err)

	assert.Contains(t, capturedSystem, "CUSTOM_RULE_XYZ",
		"system prompt must contain AGENTS.md from the workspace, not from \".\"")
}

// ------------------------------------------------------------------ regression: filtered tools don't leak

func TestLoop_FilteredToolsNotInPrompt(t *testing.T) {
	var capturedSystem string
	mock := &captureLLM{
		onChat: func(_ context.Context, req llm.Request) (*llm.Response, error) {
			capturedSystem = req.System
			return &llm.Response{Text: "ok", FinishReason: "stop"}, nil
		},
	}

	dir := t.TempDir()
	cfg := &config.Settings{Model: "m", MaxSteps: 5, MaxTokens: 1024}
	registry := tool.NewRegistry()
	// Register two tools: "allowed.tool" and "secret.tool".
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{Name: "allowed.tool", Description: "visible"},
	}))
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{Name: "secret.tool", Description: "hidden"},
	}))

	skillSvc := skill.NewServiceWithSources()
	st, _ := store.NewJSONLStore(dir, "w")
	reducer := &store.DefaultReducer{}

	ag := New(cfg, mock, registry, skillSvc, st, reducer, t.TempDir(), nil)
	_, err := ag.Run(context.Background(), "s1", llm.Input{Text: "go"}, RunOptions{
		AllowedTools: []string{"allowed.tool"},
	})
	require.NoError(t, err)

	assert.Contains(t, capturedSystem, "allowed_tool",
		"allowed tool should appear in system prompt")
	assert.NotContains(t, capturedSystem, "secret_tool",
		"filtered-out tool must NOT appear in system prompt")
}

// ------------------------------------------------------------------ session state persistence

func TestNewSession_RestoresActivatedSkills(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Text: "done", FinishReason: "stop"},
	}}
	agent, st := newTestAgent(t, mock)

	// First run: activate a skill via state entry.
	stateEntry := store.NewStateEntry(store.StatePayload{
		ActivatedSkills: map[string]store.ActivatedSkillState{
			"schema-audit": {Name: "schema-audit"},
		},
	})
	require.NoError(t, st.Append("persist-sess", stateEntry))

	// NewSession should restore the activated skill.
	sess := agent.NewSession("persist-sess", RunOptions{})
	assert.Contains(t, sess.State.ActivatedSkills, "schema-audit",
		"activated skill should be restored from store")
}

func TestNewSession_BackfillsLegacyBlockedSQLRuntimeState(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Text: "done", FinishReason: "stop"},
	}}
	agent, st := newTestAgent(t, mock)

	stateEntry := store.NewStateEntry(store.StatePayload{
		ActivatedSkills: map[string]store.ActivatedSkillState{
			"health-check": {
				Name: "health-check",
				Metadata: map[string]json.RawMessage{
					"blocked_sql": json.RawMessage(`["SHOW\\s+CREATE\\s+TABLE"]`),
				},
			},
		},
	})
	require.NoError(t, st.Append("legacy-blocked", stateEntry))

	sess := agent.NewSession("legacy-blocked", RunOptions{})
	assert.JSONEq(t, `["SHOW\\s+CREATE\\s+TABLE"]`, string(skill.ActiveBlockedSQL(sess.State)))
}

func TestNewSession_AllowlistOverridesRestoredState(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Text: "done", FinishReason: "stop"},
	}}
	agent, st := newTestAgent(t, mock)

	// Store a state with wider AllowedTools.
	stateEntry := store.NewStateEntry(store.StatePayload{
		AllowedTools: map[string]bool{"bash": true, "fs.write": true, "fs.edit": true},
	})
	require.NoError(t, st.Append("override-sess", stateEntry))

	// RunOptions with narrower allowlist should REPLACE, not merge.
	sess := agent.NewSession("override-sess", RunOptions{
		AllowedTools: []string{"bash"},
	})
	assert.True(t, sess.State.AllowedTools["bash"])
	assert.False(t, sess.State.AllowedTools["fs.write"],
		"restored fs.write should be replaced by RunOptions allowlist")
	assert.False(t, sess.State.AllowedTools["fs.edit"],
		"restored fs.edit should be replaced by RunOptions allowlist")
}

func TestLoadLatestState_PicksMostRecent(t *testing.T) {
	// Two state entries; LoadLatestState should pick the last one.
	entries := []store.Entry{
		store.NewStateEntry(store.StatePayload{
			ActivatedSkills: map[string]store.ActivatedSkillState{
				"old-skill": {Name: "old-skill"},
			},
		}),
		store.NewStateEntry(store.StatePayload{
			ActivatedSkills: map[string]store.ActivatedSkillState{
				"new-skill": {Name: "new-skill"},
			},
		}),
	}

	sp := store.LoadLatestState(entries)
	require.NotNil(t, sp)
	assert.Contains(t, sp.ActivatedSkills, "new-skill")
	assert.NotContains(t, sp.ActivatedSkills, "old-skill")
}

func TestLoadLatestState_NoStateEntries(t *testing.T) {
	entries := []store.Entry{
		store.NewUserEntry(llm.Message{Role: "user", Content: "hello"}),
	}
	sp := store.LoadLatestState(entries)
	assert.Nil(t, sp)
}

// ------------------------------------------------------------------ captureLLM mock

type captureLLM struct {
	onChat func(context.Context, llm.Request) (*llm.Response, error)
}

func (c *captureLLM) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return c.onChat(ctx, req)
}

// ------------------------------------------------------------------ regression: tape.reset stops batch

func TestLoop_ResetStopsBatchExecution(t *testing.T) {
	// Setup: register two tools:
	//  - "fake.reset" simulates tape.reset by clearing the store and returning reset meta.
	//  - "after.reset" must NOT be called after reset.
	afterResetCalled := false

	dir := t.TempDir()
	cfg := &config.Settings{Model: "m", MaxSteps: 5, MaxTokens: 1024}
	registry := tool.NewRegistry()

	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{Name: "fake.reset", Description: "simulates tape.reset"},
		Handler: func(_ context.Context, tc *tool.Context, _ json.RawMessage) (*tool.Result, error) {
			if err := tc.Store.Reset(tc.SessionID); err != nil {
				return nil, err
			}
			return &tool.Result{
				Text: "reset done",
				Meta: map[string]any{"reset": true, "skip_state_save": true},
			}, nil
		},
	}))
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{Name: "after.reset", Description: "must not be called"},
		Handler: func(_ context.Context, _ *tool.Context, _ json.RawMessage) (*tool.Result, error) {
			afterResetCalled = true
			return &tool.Result{Text: "should not happen"}, nil
		},
	}))

	// LLM returns a batch with fake.reset first, then after.reset.
	mock := &mockLLM{responses: []*llm.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "fake.reset", Arguments: json.RawMessage(`{}`)},
				{ID: "c2", Name: "after.reset", Arguments: json.RawMessage(`{}`)},
			},
		},
	}}

	skillSvc := skill.NewServiceWithSources()
	st, err := store.NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)
	reducer := &store.DefaultReducer{}

	ag := New(cfg, mock, registry, skillSvc, st, reducer, t.TempDir(), nil)

	result, err := ag.Run(context.Background(), "reset-sess", llm.Input{Text: "go"}, RunOptions{})
	require.NoError(t, err)

	// The second tool must NOT have been called.
	assert.False(t, afterResetCalled, "tool after tape.reset must not execute")

	// Result must carry reset + skip_state_save meta.
	require.NotNil(t, result.Meta)
	assert.True(t, result.Meta["reset"].(bool), "result should have reset meta")
	assert.True(t, result.Meta["skip_state_save"].(bool), "result should have skip_state_save meta")

	// Store should only have the re-seeded bootstrap anchor.
	entries, err := st.List("reset-sess", 0)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only bootstrap anchor should remain after reset")
	assert.Equal(t, store.KindAnchor, entries[0].Kind)
}
