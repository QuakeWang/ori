package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/config"
	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

func newPromptTestAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Settings{Model: "test", MaxSteps: 5, MaxTokens: 1024}
	registry := tool.NewRegistry()
	skillSvc := skill.NewServiceWithSources()
	st, err := store.NewJSONLStore(dir, "test-workspace")
	require.NoError(t, err)
	reducer := &store.DefaultReducer{}
	agent := New(cfg, &mockLLM{}, registry, skillSvc, st, reducer, t.TempDir(), nil)
	return agent, dir
}

func TestBuildSystemPrompt_ContainsBase(t *testing.T) {
	agent, _ := newPromptTestAgent(t)
	state := session.NewState("s1", ".")
	prompt := agent.buildSystemPrompt(state, RunOptions{}, nil, "")
	assert.Contains(t, prompt, "<general_instruct>")
	assert.Contains(t, prompt, "Call tools or skills to finish the task.")
}

func TestBuildSystemPrompt_ContainsOutputStyle(t *testing.T) {
	agent, _ := newPromptTestAgent(t)
	state := session.NewState("s1", ".")
	prompt := agent.buildSystemPrompt(state, RunOptions{}, nil, "")
	assert.Contains(t, prompt, "<output_style>")
	assert.Contains(t, prompt, "Answer in Chinese when user writes in Chinese.")
}

func TestBuildSystemPrompt_ContainsContextContract(t *testing.T) {
	agent, _ := newPromptTestAgent(t)
	state := session.NewState("s1", ".")
	prompt := agent.buildSystemPrompt(state, RunOptions{}, nil, "")
	assert.Contains(t, prompt, "<context_contract>")
	assert.Contains(t, prompt, "tape.handoff")
}

func TestBuildSystemPrompt_IncludesAgentsMD(t *testing.T) {
	agent, _ := newPromptTestAgent(t)

	// Create a temp workspace with AGENTS.md.
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("Custom agent rules"), 0o644))

	state := session.NewState("s1", ws)
	prompt := agent.buildSystemPrompt(state, RunOptions{}, nil, "")

	assert.Contains(t, prompt, "Custom agent rules")
	assert.Contains(t, prompt, "<user_rules>")
}

func TestBuildSystemPrompt_NoAgentsMD(t *testing.T) {
	agent, _ := newPromptTestAgent(t)
	state := session.NewState("s1", t.TempDir())
	prompt := agent.buildSystemPrompt(state, RunOptions{}, nil, "")

	assert.NotContains(t, prompt, "<user_rules>")
}

func TestBuildSystemPrompt_Order(t *testing.T) {
	agent, _ := newPromptTestAgent(t)

	// Register a tool so tool prompt is non-empty.
	require.NoError(t, agent.tools.Register(&tool.Tool{
		Spec: tool.Spec{Name: "test.tool", Description: "a test tool"},
	}))

	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("RULES"), 0o644))

	state := session.NewState("s1", ws)
	prompt := agent.buildSystemPrompt(state, RunOptions{}, nil, "")

	// Verify ordering: base prompt < AGENTS.md < tool prompt.
	baseIdx := strings.Index(prompt, "<general_instruct>")
	rulesIdx := strings.Index(prompt, "RULES")
	toolIdx := strings.Index(prompt, "test_tool")

	assert.True(t, baseIdx < rulesIdx, "base prompt should come before AGENTS.md")
	assert.True(t, rulesIdx < toolIdx, "AGENTS.md should come before tool prompt")
}

func TestBuildSystemPrompt_HintedSkillIsPromptLocal(t *testing.T) {
	agent, _ := newPromptTestAgent(t)
	state := session.NewState("s1", t.TempDir())
	skills := []skill.Skill{
		{
			Name:        "health-check",
			Description: "Health",
			Body:        "Follow health workflow.",
		},
	}

	prompt := agent.buildSystemPrompt(state, RunOptions{}, skills, "please use $health-check")

	assert.Contains(t, prompt, `<active_skill_instructions name="health-check">`)
	assert.Empty(t, state.ActivatedSkills)
}
