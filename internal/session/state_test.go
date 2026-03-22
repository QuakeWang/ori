package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewState_InitializedMaps(t *testing.T) {
	s := NewState("sess-1", "/workspace")

	assert.Equal(t, "sess-1", s.SessionID)
	assert.Equal(t, "/workspace", s.Workspace)
	require.NotNil(t, s.ActivatedSkills)
	assert.Empty(t, s.ActivatedSkills)
	// AllowedTools/AllowedSkills are nil by default (= no restriction).
	assert.Nil(t, s.AllowedTools, "nil = no restriction")
	assert.Nil(t, s.AllowedSkills, "nil = no restriction")
	require.NotNil(t, s.Extras)
}

func TestState_ActivatedSkillsRoundTrip(t *testing.T) {
	s := NewState("sess-1", "/workspace")

	override := 10
	s.ActivatedSkills["test-skill"] = Activation{
		Name:             "test-skill",
		MaxStepsOverride: &override,
		Metadata: map[string]json.RawMessage{
			"key": json.RawMessage(`"value"`),
		},
	}

	act, ok := s.ActivatedSkills["test-skill"]
	require.True(t, ok)
	assert.Equal(t, "test-skill", act.Name)
	require.NotNil(t, act.MaxStepsOverride)
	assert.Equal(t, 10, *act.MaxStepsOverride)
	assert.JSONEq(t, `"value"`, string(act.Metadata["key"]))
}

func TestState_AllowedToolsFilter(t *testing.T) {
	s := NewState("sess-1", "/workspace")
	// Simulate explicit tool allowlist.
	s.AllowedTools = map[string]bool{
		"bash":    true,
		"fs.read": true,
	}

	assert.True(t, s.AllowedTools["bash"])
	assert.True(t, s.AllowedTools["fs.read"])
	assert.False(t, s.AllowedTools["fs.write"])
}

func TestState_AllowedSkillsDenyAll(t *testing.T) {
	s := NewState("sess-1", "/workspace")
	// Non-nil empty map = deny all.
	s.AllowedSkills = make(map[string]bool)
	assert.NotNil(t, s.AllowedSkills)
	assert.False(t, s.AllowedSkills["any-skill"])
}

func TestAllowlistFromSlice(t *testing.T) {
	assert.Nil(t, AllowlistFromSlice(nil))
	assert.Empty(t, AllowlistFromSlice([]string{}))

	allowed := AllowlistFromSlice([]string{"bash", "fs.read"})
	assert.True(t, allowed["bash"])
	assert.True(t, allowed["fs.read"])
}

func TestAllowlistToSlice(t *testing.T) {
	assert.Empty(t, AllowlistToSlice(map[string]bool{}))

	items := AllowlistToSlice(map[string]bool{
		"slow-query":   true,
		"health-check": true,
	})
	assert.Equal(t, []string{"health-check", "slow-query"}, items)
}
