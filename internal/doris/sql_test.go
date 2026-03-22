package doris

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
)

func TestIsReadOnly(t *testing.T) {
	assert.True(t, IsReadOnly("SELECT 1"))
	assert.True(t, IsReadOnly(" show frontends ; "))
	assert.True(t, IsReadOnly("EXPLAIN SELECT * FROM t"))
	assert.True(t, IsReadOnly("ADMIN SHOW FRONTENDS"))
	assert.True(t, IsReadOnly("ADMIN DIAGNOSE TABLET 1"))
	assert.True(t, IsReadOnly("HELP SHOW"))
}

func TestIsReadOnly_BlocksDangerousSQL(t *testing.T) {
	assert.False(t, IsReadOnly(""))
	assert.False(t, IsReadOnly("INSERT INTO t VALUES (1)"))
	assert.False(t, IsReadOnly("DELETE FROM t"))
	assert.False(t, IsReadOnly("ADMIN SET FRONTEND CONFIG ('k'='v')"))
	assert.False(t, IsReadOnly("ADMIN COMPACT TABLE t"))
}

func TestIsSetStatement(t *testing.T) {
	assert.True(t, IsSetStatement("SET enable_profile = true"))
	assert.True(t, IsSetStatement("SET SESSION query_timeout = 10"))
	assert.True(t, IsSetStatement("SET @@session.enable_profile = true"))
}

func TestIsSetStatement_RejectsUnsafeOrOutOfContractForms(t *testing.T) {
	assert.False(t, IsSetStatement(""))
	assert.False(t, IsSetStatement("SET GLOBAL query_timeout = 10"))
	assert.False(t, IsSetStatement("SET names utf8mb4"))
	assert.False(t, IsSetStatement("SET @user_var = 1"))
	assert.False(t, IsSetStatement("SET enable_profile = true; SELECT 1"))
}

func TestBlockedPatternsFromState(t *testing.T) {
	state := session.NewState("sess-1", t.TempDir())
	act := session.Activation{
		Name: "health-check",
		Metadata: map[string]json.RawMessage{
			"blocked_sql": json.RawMessage(`["SHOW\\s+CREATE\\s+TABLE","SHOW\\s+PARTITIONS"]`),
		},
	}
	state.ActivatedSkills["health-check"] = act
	skill.ApplyActivationRuntimeState(state, act)

	patterns, err := BlockedPatternsFromState(state)
	require.NoError(t, err)
	require.Len(t, patterns, 2)

	err = CheckBlockedPatterns("show create table db.tbl", patterns)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedSQL)

	err = CheckBlockedPatterns("SHOW FRONTENDS", patterns)
	require.NoError(t, err)
}

func TestBlockedPatternsFromState_LastExplicitSkillWins(t *testing.T) {
	state := session.NewState("sess-1", t.TempDir())

	healthCheck := session.Activation{
		Name: "health-check",
		Metadata: map[string]json.RawMessage{
			"blocked_sql": json.RawMessage(`["SHOW\\s+CREATE\\s+TABLE","SHOW\\s+PARTITIONS"]`),
		},
	}
	state.ActivatedSkills["health-check"] = healthCheck
	skill.ApplyActivationRuntimeState(state, healthCheck)

	schemaAudit := session.Activation{Name: "schema-audit"}
	state.ActivatedSkills["schema-audit"] = schemaAudit
	skill.ApplyActivationRuntimeState(state, schemaAudit)

	patterns, err := BlockedPatternsFromState(state)
	require.NoError(t, err)
	assert.Empty(t, patterns)
}

func TestBlockedPatternsFromState_InvalidRegex(t *testing.T) {
	state := session.NewState("sess-1", t.TempDir())
	act := session.Activation{
		Name: "bad-skill",
		Metadata: map[string]json.RawMessage{
			"blocked_sql": json.RawMessage(`["SHOW("]`),
		},
	}
	state.ActivatedSkills["bad-skill"] = act
	skill.ApplyActivationRuntimeState(state, act)

	_, err := BlockedPatternsFromState(state)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compile active blocked_sql")
}
