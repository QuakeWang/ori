package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

// ------------------------------------------------------------------ command mode integration tests

func newCommandAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Settings{Model: "m", MaxSteps: 5, MaxTokens: 1024}
	registry := tool.NewRegistry()

	// Register echo tool.
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{
			Name:        "echo",
			Description: "echoes text",
			Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		},
		Handler: func(_ context.Context, _ *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in struct{ Text string }
			_ = json.Unmarshal(input, &in)
			return &tool.Result{Text: "echo: " + in.Text}, nil
		},
	}))

	// Register a bash stub that confirms it got the "cmd" field.
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{
			Name:        "bash",
			Description: "shell",
			Schema:      json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}`),
		},
		Handler: func(_ context.Context, _ *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in struct {
				Cmd string `json:"cmd"`
			}
			_ = json.Unmarshal(input, &in)
			return &tool.Result{Text: "bash: " + in.Cmd}, nil
		},
	}))

	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{
			Name:        "typed",
			Description: "checks inferred argument types",
			Schema:      json.RawMessage(`{"type":"object","properties":{"background":{"type":"boolean"},"limit":{"type":"integer"},"name":{"type":"string"}}}`),
		},
		Handler: func(_ context.Context, _ *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in struct {
				Background bool   `json:"background"`
				Limit      int    `json:"limit"`
				Name       string `json:"name"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			return &tool.Result{
				Text: fmt.Sprintf("background=%t limit=%d name=%s", in.Background, in.Limit, in.Name),
			}, nil
		},
	}))

	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{
			Name:        "danger.write",
			Description: "dangerous command-mode tool",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			Dangerous:   true,
		},
		Handler: func(_ context.Context, _ *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(input, &in)
			return &tool.Result{Text: "danger: " + in.Path}, nil
		},
	}))

	skillSvc := skill.NewServiceWithSources()
	st, _ := store.NewJSONLStore(dir, "w")
	reducer := &store.DefaultReducer{}
	return New(cfg, nil, registry, skillSvc, st, reducer, t.TempDir(), fmt.Errorf("no llm configured"))
}

func TestCommandMode_RegisteredTool(t *testing.T) {
	ag := newCommandAgent(t)
	// ,echo text=hello → echo tool gets {"text":"hello"}
	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",echo text=hello"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "echo: hello", result.Output)
}

func TestCommandMode_UnknownReturnsError(t *testing.T) {
	ag := newCommandAgent(t)
	// ,ls -la → unknown command → error, NOT bash execution
	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",ls -la"}, RunOptions{})
	require.NoError(t, err)
	assert.Contains(t, result.Output, "Unknown command")
	assert.Contains(t, result.Output, ",bash cmd=...")
	assert.NotContains(t, result.Output, "bash: ls -la",
		"unknown command must NOT fall back to bash")
}

func TestCommandMode_UnknownToolPrefixShowsSuggestions(t *testing.T) {
	ag := newCommandAgent(t)
	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",dang"}, RunOptions{})
	require.NoError(t, err)
	assert.Contains(t, result.Output, "Unknown command ',dang'.")
	assert.Contains(t, result.Output, ",danger.write")
	assert.NotContains(t, result.Output, "bash:")
}

func TestCommandMode_PositionalAfterKeywordErrors(t *testing.T) {
	ag := newCommandAgent(t)
	// ,echo text=hello world → positional 'world' after keyword → error
	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",echo text=hello world"}, RunOptions{})
	require.NoError(t, err) // runCommand returns error in Output, not as Go error
	assert.Contains(t, result.Output, "Error:",
		"positional after keyword should produce an error")
}

func TestCommandMode_BashDirectWithKeyword(t *testing.T) {
	ag := newCommandAgent(t)
	// ,bash cmd='ls -la' → bash tool gets {"cmd":"ls -la"}
	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",bash cmd='ls -la'"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "bash: ls -la", result.Output)
}

func TestCommandMode_InferBoolAndIntegerArgs(t *testing.T) {
	ag := newCommandAgent(t)
	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",typed background=true limit=3 name='007'"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "background=true limit=3 name=007", result.Output)
}

func TestCommandMode_AllowsDangerousUserCommands(t *testing.T) {
	ag := newCommandAgent(t)
	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",danger.write path=notes.txt"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "danger: notes.txt", result.Output)
}

func TestRunTurn_CommandModeFallsBackToAgentStore_WhenSessionStoreIsNil(t *testing.T) {
	ag := newCommandAgent(t)
	sess := session.New("embedded-command-sess", ag.workspace)

	result, err := ag.RunTurn(context.Background(), sess, llm.Input{Text: ",echo text=hello"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "echo: hello", result.Output)

	entries, err := ag.store.List("embedded-command-sess", 0)
	require.NoError(t, err)
	require.Len(t, entries, 3, "command mode should persist anchor, event, and state via fallback store")
	assert.Equal(t, store.KindAnchor, entries[0].Kind)
	assert.Equal(t, store.KindEvent, entries[1].Kind)
	assert.Equal(t, store.KindState, entries[2].Kind)
}

func TestCommandMode_PassesDiscoveredSkillsToToolContext(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".agents", "skills", "health-check")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(
		"---\nname: health-check\ndescription: Health check\n---\nBody",
	), 0o644))

	dir := t.TempDir()
	cfg := &config.Settings{Model: "m", MaxSteps: 5, MaxTokens: 1024}
	registry := tool.NewRegistry()
	require.NoError(t, registry.Register(&tool.Tool{
		Spec: tool.Spec{Name: "inspect.skills", Description: "reports discovered skills"},
		Handler: func(_ context.Context, tc *tool.Context, _ json.RawMessage) (*tool.Result, error) {
			return &tool.Result{Text: fmt.Sprintf("skills=%d", len(tc.Skills))}, nil
		},
	}))

	skillSvc := skill.NewServiceWithSources()
	st, _ := store.NewJSONLStore(dir, "w")
	reducer := &store.DefaultReducer{}
	ag := New(cfg, nil, registry, skillSvc, st, reducer, workspace, fmt.Errorf("no llm configured"))

	result, err := ag.Run(context.Background(), "s1", llm.Input{Text: ",inspect.skills"}, RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "skills=1", result.Output)
}
