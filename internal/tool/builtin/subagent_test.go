package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/tool"
)

// ------------------------------------------------------------------ resolveSubagentSession

func TestResolveSubagentSession_Temp(t *testing.T) {
	sid := resolveSubagentSession("temp", "cli:local")
	assert.True(t, strings.HasPrefix(sid, "temp/"), "temp session should have temp/ prefix")
	assert.Greater(t, len(sid), len("temp/"), "should have a random suffix")
}

func TestResolveSubagentSession_EmptyDefaultsToTemp(t *testing.T) {
	sid := resolveSubagentSession("", "cli:local")
	assert.True(t, strings.HasPrefix(sid, "temp/"))
}

func TestResolveSubagentSession_Inherit(t *testing.T) {
	sid := resolveSubagentSession("inherit", "cli:local")
	assert.Equal(t, "cli:local", sid)
}

func TestResolveSubagentSession_Custom(t *testing.T) {
	sid := resolveSubagentSession("my-session", "cli:local")
	assert.Equal(t, "my-session", sid)
}

func TestResolveSubagentSession_UniqueIDs(t *testing.T) {
	a := resolveSubagentSession("temp", "p")
	b := resolveSubagentSession("temp", "p")
	assert.NotEqual(t, a, b, "each temp session should get a unique ID")
}

// ------------------------------------------------------------------ subagentTool handler

func TestSubagentTool_EmptyPromptError(t *testing.T) {
	runner := func(_ context.Context, _, _, _ string, _, _ []string, _ *session.State) (string, error) {
		t.Fatal("runner should not be called with empty prompt")
		return "", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{}
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{"prompt":""}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is required")
}

func TestSubagentTool_InvalidInputError(t *testing.T) {
	runner := func(_ context.Context, _, _, _ string, _, _ []string, _ *session.State) (string, error) {
		t.Fatal("runner should not be called with invalid input")
		return "", nil
	}
	tl := subagentTool(runner)

	_, err := tl.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{"prompt":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid subagent input")
}

func TestSubagentTool_BasicExecution(t *testing.T) {
	var capturedSession, capturedPrompt, capturedModel string
	runner := func(_ context.Context, sid, prompt, model string, _, _ []string, _ *session.State) (string, error) {
		capturedSession = sid
		capturedPrompt = prompt
		capturedModel = model
		return "sub-result", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{SessionID: "parent-session"}
	result, err := tl.Handler(context.Background(), tc, json.RawMessage(`{
		"prompt": "analyze this table",
		"model": "gpt-4o",
		"session": "inherit"
	}`))
	require.NoError(t, err)
	assert.Equal(t, "sub-result", result.Text)
	assert.Equal(t, "parent-session", capturedSession)
	assert.Equal(t, "analyze this table", capturedPrompt)
	assert.Equal(t, "gpt-4o", capturedModel)
}

func TestSubagentTool_TempSessionByDefault(t *testing.T) {
	var capturedSession string
	runner := func(_ context.Context, sid, _, _ string, _, _ []string, _ *session.State) (string, error) {
		capturedSession = sid
		return "ok", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{SessionID: "parent"}
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{"prompt":"task"}`))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(capturedSession, "temp/"))
}

func TestSubagentTool_SkillInheritance(t *testing.T) {
	var capturedSkills []string
	runner := func(_ context.Context, _, _, _ string, _ []string, skills []string, _ *session.State) (string, error) {
		capturedSkills = skills
		return "ok", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{
		SessionID: "s1",
		State: &session.State{
			ActivatedSkills: map[string]session.Activation{
				"health-check": {Name: "health-check"},
				"slow-query":   {Name: "slow-query"},
			},
		},
	}
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{"prompt":"diagnose"}`))
	require.NoError(t, err)
	assert.Len(t, capturedSkills, 2)
	assert.Contains(t, capturedSkills, "health-check")
	assert.Contains(t, capturedSkills, "slow-query")
}

func TestSubagentTool_ExplicitSkillsOverrideInheritance(t *testing.T) {
	var capturedSkills []string
	runner := func(_ context.Context, _, _, _ string, _ []string, skills []string, _ *session.State) (string, error) {
		capturedSkills = skills
		return "ok", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{
		SessionID: "s1",
		State: &session.State{
			ActivatedSkills: map[string]session.Activation{
				"health-check": {Name: "health-check"},
			},
		},
	}
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{
		"prompt": "audit",
		"allowed_skills": ["schema-audit"]
	}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"schema-audit"}, capturedSkills)
}

func TestSubagentTool_EmptySkillsDeniesInheritance(t *testing.T) {
	var capturedSkills []string
	runner := func(_ context.Context, _, _, _ string, _ []string, skills []string, _ *session.State) (string, error) {
		capturedSkills = skills
		return "ok", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{
		SessionID: "s1",
		State: &session.State{
			ActivatedSkills: map[string]session.Activation{
				"health-check": {Name: "health-check"},
			},
		},
	}
	// Explicitly empty array — should NOT inherit parent skills.
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{
		"prompt": "raw task",
		"allowed_skills": []
	}`))
	require.NoError(t, err)
	assert.Empty(t, capturedSkills, "empty allowed_skills should not inherit parent skills")
}

// User-facing contract: {"allowed_tools": []} must deny ALL tools,
// even when parent has tools available.
func TestSubagentTool_ExplicitEmptyToolsDeniesAll(t *testing.T) {
	var capturedTools []string
	runner := func(_ context.Context, _, _, _ string, tools []string, _ []string, _ *session.State) (string, error) {
		capturedTools = tools
		return "ok", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{
		SessionID: "s1",
		State: &session.State{
			AllowedTools: map[string]bool{"bash": true, "fs.read": true},
		},
	}
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{
		"prompt": "locked down task",
		"allowed_tools": []
	}`))
	require.NoError(t, err)
	require.NotNil(t, capturedTools, "explicit [] must produce non-nil empty slice, not nil")
	assert.Empty(t, capturedTools, "allowed_tools: [] must deny all tools")
}

func TestSubagentTool_ParentDenyAllToolsPropagates(t *testing.T) {
	var capturedTools []string
	runner := func(_ context.Context, _, _, _ string, tools []string, _ []string, _ *session.State) (string, error) {
		capturedTools = tools
		return "ok", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{
		SessionID: "s1",
		State: &session.State{
			AllowedTools: map[string]bool{}, // deny-all
		},
	}
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{"prompt":"task"}`))
	require.NoError(t, err)
	// Should propagate deny-all: non-nil empty slice (not nil = unrestricted).
	require.NotNil(t, capturedTools, "deny-all tools should propagate as non-nil empty slice")
	assert.Empty(t, capturedTools, "deny-all tools should be empty")
}

func TestSubagentTool_ParentDenyAllSkillsPropagates(t *testing.T) {
	var capturedSkills []string
	runner := func(_ context.Context, _, _, _ string, _ []string, skills []string, _ *session.State) (string, error) {
		capturedSkills = skills
		return "ok", nil
	}
	tl := subagentTool(runner)

	tc := &tool.Context{
		SessionID: "s1",
		State: &session.State{
			AllowedSkills: map[string]bool{}, // deny-all
			ActivatedSkills: map[string]session.Activation{
				"health-check": {Name: "health-check"},
			},
		},
	}
	_, err := tl.Handler(context.Background(), tc, json.RawMessage(`{"prompt":"task"}`))
	require.NoError(t, err)
	// Should propagate deny-all despite parent having activated skills.
	require.NotNil(t, capturedSkills, "deny-all skills should propagate as non-nil empty slice")
	assert.Empty(t, capturedSkills, "deny-all skills should not inherit activated skills")
}

// ------------------------------------------------------------------ RegisterSubagent

func TestRegisterSubagent(t *testing.T) {
	registry := tool.NewRegistry()
	// Pre-register stub (as RegisterCore does).
	require.NoError(t, registry.Register(subagentStub()))

	runner := func(_ context.Context, _, _, _ string, _, _ []string, _ *session.State) (string, error) {
		return "real", nil
	}
	// Replace must not panic and should upgrade the stub.
	RegisterSubagent(registry, runner)

	tl, found := registry.Get("subagent")
	assert.True(t, found, "subagent tool should be registered")
	// Verify the real handler is wired, not the stub.
	result, err := tl.Handler(context.Background(), &tool.Context{
		State: session.NewState("test", "/tmp"),
	}, json.RawMessage(`{"prompt":"hello"}`))
	require.NoError(t, err)
	assert.Equal(t, "real", result.Text)
}

func TestRegisterSubagent_IdempotentReplace(t *testing.T) {
	registry := tool.NewRegistry()
	require.NoError(t, registry.Register(subagentStub()))

	runner := func(_ context.Context, _, _, _ string, _, _ []string, _ *session.State) (string, error) {
		return "", nil
	}
	// Multiple Replace calls should not panic.
	RegisterSubagent(registry, runner)
	RegisterSubagent(registry, runner)
	_, found := registry.Get("subagent")
	assert.True(t, found)
}
