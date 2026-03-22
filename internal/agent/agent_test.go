package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

func TestInheritParentState_SkillWhitelistFilters(t *testing.T) {
	parent := session.NewState("parent", "/workspace")
	parent.ActivatedSkills["health-check"] = session.Activation{Name: "health-check"}
	parent.ActivatedSkills["slow-query"] = session.Activation{Name: "slow-query"}
	parent.ActivatedSkills["schema-audit"] = session.Activation{Name: "schema-audit"}

	child := session.NewState("child", "/workspace")
	// Only allow health-check and slow-query.
	child.AllowedSkills = map[string]bool{
		"health-check": true,
		"slow-query":   true,
	}

	inheritParentState(child, parent)

	assert.Len(t, child.ActivatedSkills, 2)
	assert.Contains(t, child.ActivatedSkills, "health-check")
	assert.Contains(t, child.ActivatedSkills, "slow-query")
	assert.NotContains(t, child.ActivatedSkills, "schema-audit", "should be filtered by AllowedSkills")
}

func TestInheritParentState_DenyAllSkills(t *testing.T) {
	parent := session.NewState("parent", "/workspace")
	parent.ActivatedSkills["health-check"] = session.Activation{Name: "health-check"}

	child := session.NewState("child", "/workspace")
	child.AllowedSkills = make(map[string]bool) // deny all

	inheritParentState(child, parent)

	assert.Empty(t, child.ActivatedSkills, "deny-all should block all skill inheritance")
}

func TestInheritParentState_NoWhitelistInheritsAll(t *testing.T) {
	parent := session.NewState("parent", "/workspace")
	parent.ActivatedSkills["health-check"] = session.Activation{Name: "health-check"}
	parent.ActivatedSkills["slow-query"] = session.Activation{Name: "slow-query"}

	child := session.NewState("child", "/workspace")
	// AllowedSkills is nil → no restriction → inherit all.

	inheritParentState(child, parent)

	assert.Len(t, child.ActivatedSkills, 2)
}

func TestInheritParentState_ExtrasInherited(t *testing.T) {
	parent := session.NewState("parent", "/workspace")
	parent.Extras["budget"] = json.RawMessage(`100`)
	parent.Extras["channel"] = json.RawMessage(`"slack"`)

	child := session.NewState("child", "/workspace")
	child.Extras["budget"] = json.RawMessage(`50`) // pre-existing, should NOT be overwritten

	inheritParentState(child, parent)

	assert.JSONEq(t, `50`, string(child.Extras["budget"]), "pre-existing key should not be overwritten")
	assert.JSONEq(t, `"slack"`, string(child.Extras["channel"]), "new key should be inherited")
}

func TestInheritParentState_NilSafe(t *testing.T) {
	// Should not panic with nil parent or child.
	inheritParentState(nil, session.NewState("p", "/w"))
	inheritParentState(session.NewState("c", "/w"), nil)
	inheritParentState(nil, nil)
}

// ------------------------------------------------------------------ persistence round-trip

func TestStatePayload_DenyAllToolsSurvivesRoundTrip(t *testing.T) {
	// Scenario: session has deny-all for tools → save → restore into fresh state.
	original := session.NewState("s1", "/workspace")
	original.AllowedTools = make(map[string]bool) // deny-all
	original.AllowedSkills = map[string]bool{"only-this": true}

	// Save → restore.
	sp := buildStatePayload(original)
	restored := session.NewState("s1", "/workspace")
	applyStatePayload(restored, &sp)

	assert.NotNil(t, restored.AllowedTools, "deny-all AllowedTools must survive round-trip")
	assert.Empty(t, restored.AllowedTools, "deny-all AllowedTools must be empty after restore")
	assert.True(t, restored.AllowedSkills["only-this"])
}

func TestStatePayload_DenyAllSkillsSurvivesRoundTrip(t *testing.T) {
	original := session.NewState("s1", "/workspace")
	original.AllowedSkills = make(map[string]bool) // deny-all
	original.AllowedTools = map[string]bool{"bash": true}

	sp := buildStatePayload(original)
	restored := session.NewState("s1", "/workspace")
	applyStatePayload(restored, &sp)

	assert.NotNil(t, restored.AllowedSkills, "deny-all AllowedSkills must survive round-trip")
	assert.Empty(t, restored.AllowedSkills, "deny-all AllowedSkills must be empty after restore")
	assert.True(t, restored.AllowedTools["bash"])
}

func TestStatePayload_NilAllowlistPreserved(t *testing.T) {
	// Scenario: session has nil AllowedTools → save → restore must stay nil.
	original := session.NewState("s1", "/workspace")
	// AllowedTools/AllowedSkills are nil by default.

	sp := buildStatePayload(original)
	restored := session.NewState("s1", "/workspace")
	applyStatePayload(restored, &sp)

	assert.Nil(t, restored.AllowedTools, "nil AllowedTools must stay nil after round-trip")
	assert.Nil(t, restored.AllowedSkills, "nil AllowedSkills must stay nil after round-trip")
}

func TestStatePayload_JSONRoundTrip(t *testing.T) {
	// End-to-end: build → JSON marshal → unmarshal → apply → verify.
	original := session.NewState("s1", "/workspace")
	original.AllowedTools = make(map[string]bool) // deny-all
	original.AllowedSkills = nil                  // unrestricted

	sp := buildStatePayload(original)
	data, err := json.Marshal(sp)
	assert.NoError(t, err)

	// Verify JSON contains "allowed_tools": {} (not omitted).
	assert.Contains(t, string(data), `"allowed_tools":{}`)
	// Verify JSON omits allowed_skills (nil → null → Go json: null for map = nil).
	// Actually Go marshals nil map as "null", which on unmarshal becomes nil.
	assert.Contains(t, string(data), `"allowed_skills":null`)

	var restored store.StatePayload
	assert.NoError(t, json.Unmarshal(data, &restored))

	state := session.NewState("s1", "/workspace")
	applyStatePayload(state, &restored)

	assert.NotNil(t, state.AllowedTools, "deny-all must survive JSON round-trip")
	assert.Empty(t, state.AllowedTools)
	assert.Nil(t, state.AllowedSkills, "nil must survive JSON round-trip")
}

func TestBuildSubagentToolList_DefaultExcludesSubagentAndDangerous(t *testing.T) {
	registry := tool.NewRegistry()
	assert.NoError(t, registry.Register(&tool.Tool{Spec: tool.Spec{Name: "safe.read"}}))
	assert.NoError(t, registry.Register(&tool.Tool{Spec: tool.Spec{Name: "subagent"}}))
	assert.NoError(t, registry.Register(&tool.Tool{Spec: tool.Spec{Name: "danger.write", Dangerous: true}}))

	ag := &Agent{tools: registry}
	allowed := ag.buildSubagentToolList(nil)

	assert.Equal(t, []string{"safe.read"}, allowed)
}

func TestBuildSubagentToolList_ExplicitListPreservesDenyAllAndFiltersSubagent(t *testing.T) {
	ag := &Agent{}

	assert.Empty(t, ag.buildSubagentToolList([]string{}))
	assert.Equal(t, []string{"bash", "fs.read"}, ag.buildSubagentToolList([]string{"bash", "subagent", "fs.read"}))
}
