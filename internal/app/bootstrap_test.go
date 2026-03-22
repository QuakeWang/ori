package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/agent"
	"github.com/QuakeWang/ori/internal/config"
	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/tool"
)

func TestBuild_AllowsCommandOnlyWithoutLLMProvider(t *testing.T) {
	cfg := &config.Settings{
		Home:      t.TempDir(),
		Model:     "openrouter:qwen/qwen3-coder-next",
		MaxSteps:  5,
		MaxTokens: 1024,
	}

	ag, err := Build(cfg, t.TempDir())
	require.NoError(t, err)

	result, err := ag.Run(context.Background(), "cli:local", llm.Input{Text: ",help"}, agent.RunOptions{})
	require.NoError(t, err)
	require.Contains(t, result.Output, ",help")
}

func TestBuildReadOnly_IncludesDorisExtensions(t *testing.T) {
	ro, err := BuildReadOnly()
	require.NoError(t, err)

	_, ok := ro.Registry.Get("doris.ping")
	require.True(t, ok)
	_, ok = ro.Registry.Get("doris.sql")
	require.True(t, ok)

	helpText := runHelpTool(t, ro.Registry)
	require.Contains(t, helpText, ",doris.ping")
	require.Contains(t, helpText, ",doris.sql sql='SHOW FRONTENDS'")
	require.Contains(t, helpText, ",doris.profile query_id=")

	skills, err := ro.Skills.Discover(t.TempDir())
	require.NoError(t, err)
	requireSkillNames(t, skills,
		"explain-analyze",
		"health-check",
		"schema-audit",
		"skill-creator",
		"slow-query",
	)
}

func runHelpTool(t *testing.T, registry *tool.Registry) string {
	t.Helper()

	helpTool, ok := registry.Get("help")
	require.True(t, ok)

	result, err := helpTool.Handler(context.Background(), &tool.Context{}, nil)
	require.NoError(t, err)
	return result.Text
}

func requireSkillNames(t *testing.T, skills []skill.Skill, expected ...string) {
	t.Helper()

	names := make(map[string]bool, len(skills))
	for _, sk := range skills {
		names[sk.Name] = true
	}
	for _, name := range expected {
		require.True(t, names[name], "expected builtin skill %q to be discovered", name)
	}
}
