package doris

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/tool"
)

type fakeConn struct {
	pingInfo        PingInfo
	pingErr         error
	resultSet       *ResultSet
	queryErr        error
	lastQuery       string
	sessionSetupSQL []string
	closed          bool
}

func (f *fakeConn) Ping(ctx context.Context) (PingInfo, error) {
	return f.pingInfo, f.pingErr
}

func (f *fakeConn) Query(ctx context.Context, query string) (*ResultSet, error) {
	f.lastQuery = query
	return f.resultSet, f.queryErr
}

func (f *fakeConn) Close() error {
	f.closed = true
	return nil
}

func (f *fakeConn) SessionQuery(ctx context.Context, setupSQLs []string, query string) (*ResultSet, error) {
	f.sessionSetupSQL = append([]string(nil), setupSQLs...)
	f.lastQuery = query
	return f.resultSet, f.queryErr
}

func TestPingTool(t *testing.T) {
	t.Setenv("DORIS_FE_HOST", "fe.example")
	t.Setenv("DORIS_FE_PORT", "9130")

	conn := &fakeConn{
		pingInfo: PingInfo{
			User:     "   ",
			Database: "",
			Version:  "\t",
		},
	}

	handler := newPingTool(func(cfg Config) (Conn, error) {
		assert.Equal(t, "fe.example", cfg.Host)
		assert.Equal(t, 9130, cfg.Port)
		return conn, nil
	})

	result, err := handler.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, result.Text, "OK: connected to fe.example:9130")
	assert.Contains(t, result.Text, "user: ?")
	assert.Contains(t, result.Text, "database: (none)")
	assert.Contains(t, result.Text, "version: ?")
	assert.True(t, conn.closed)
}

func TestSQLTool_ReadOnlyReject(t *testing.T) {
	handler := newSQLTool(func(cfg Config) (Conn, error) {
		t.Fatal("open should not be called for blocked write SQL")
		return nil, nil
	})

	_, err := handler.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{"sql":"INSERT INTO t VALUES (1)"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only read-only SQL is allowed")
}

func TestSQLTool_SkillBlockedQuery(t *testing.T) {
	state := session.NewState("sess-1", t.TempDir())
	act := session.Activation{
		Name: "health-check",
		Metadata: map[string]json.RawMessage{
			"blocked_sql": json.RawMessage(`["SHOW\\s+CREATE\\s+TABLE"]`),
		},
	}
	state.ActivatedSkills["health-check"] = act
	skill.ApplyActivationRuntimeState(state, act)

	handler := newSQLTool(func(cfg Config) (Conn, error) {
		t.Fatal("open should not be called for skill-blocked SQL")
		return nil, nil
	})

	result, err := handler.Handler(
		context.Background(),
		&tool.Context{State: state},
		json.RawMessage(`{"sql":"SHOW CREATE TABLE db.tbl"}`),
	)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "(blocked:")
}

func TestSQLTool_QueryErrorReturnsToolText(t *testing.T) {
	conn := &fakeConn{queryErr: errors.New("syntax error")}
	handler := newSQLTool(func(cfg Config) (Conn, error) {
		return conn, nil
	})

	result, err := handler.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{"sql":"SELECT 1"}`))
	require.NoError(t, err)
	assert.Contains(t, result.Text, "(error:")
	assert.Contains(t, result.Text, "SQL: SELECT 1")
	assert.True(t, conn.closed)
}

func TestSQLTool_UsesDatabaseOverrideAndColumnFilter(t *testing.T) {
	t.Setenv("DORIS_DATABASE", "default_db")

	conn := &fakeConn{
		resultSet: &ResultSet{
			Columns: []string{"db", "tbl", "rows"},
			Rows: []map[string]string{
				{"db": "analytics", "tbl": "events", "rows": "100"},
				{"db": "analytics", "tbl": "users", "rows": "50"},
			},
		},
	}

	handler := newSQLTool(func(cfg Config) (Conn, error) {
		assert.Equal(t, "analytics", cfg.Database)
		return conn, nil
	})

	result, err := handler.Handler(
		context.Background(),
		&tool.Context{},
		json.RawMessage(`{"sql":"SELECT * FROM information_schema.tables","database":"analytics","columns":"tbl","max_rows":1}`),
	)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM information_schema.tables", conn.lastQuery)
	assert.Contains(t, result.Text, "| tbl |")
	assert.NotContains(t, result.Text, "| db |")
	assert.Contains(t, result.Text, "(1 more rows truncated)")
	assert.True(t, result.Truncated)
	assert.True(t, conn.closed)
}

func TestSQLSessionTool_UsesValidatedSetupSQLs(t *testing.T) {
	conn := &fakeConn{
		resultSet: &ResultSet{
			Columns: []string{"k"},
			Rows:    []map[string]string{{"k": "v"}},
		},
	}
	handler := newSQLSessionTool(func(cfg Config) (Conn, error) {
		return conn, nil
	})

	result, err := handler.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{
		"setup_sqls":["SET enable_profile = true","SET SESSION query_timeout = 5"],
		"sql":"SELECT 1"
	}`))
	require.NoError(t, err)
	assert.Contains(t, result.Text, "| k |")
	assert.Equal(t, []string{"SET enable_profile = true", "SET SESSION query_timeout = 5"}, conn.sessionSetupSQL)
	assert.Equal(t, "SELECT 1", conn.lastQuery)
	assert.True(t, conn.closed)
}

func TestSQLSessionTool_RejectsNonSessionSetupSQL(t *testing.T) {
	handler := newSQLSessionTool(func(cfg Config) (Conn, error) {
		t.Fatal("open should not be called for invalid setup SQL")
		return nil, nil
	})

	_, err := handler.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{
		"setup_sqls":["SET GLOBAL query_timeout = 5"],
		"sql":"SELECT 1"
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single session variable SET assignments")
}

func TestSQLSessionTool_BlockedSetupSQL(t *testing.T) {
	state := session.NewState("sess-1", t.TempDir())
	act := session.Activation{
		Name: "explain-analyze",
		Metadata: map[string]json.RawMessage{
			"blocked_sql": json.RawMessage(`["SET\\s+ENABLE_PROFILE"]`),
		},
	}
	state.ActivatedSkills["explain-analyze"] = act
	skill.ApplyActivationRuntimeState(state, act)

	handler := newSQLSessionTool(func(cfg Config) (Conn, error) {
		t.Fatal("open should not be called for skill-blocked setup SQL")
		return nil, nil
	})

	result, err := handler.Handler(context.Background(), &tool.Context{State: state}, json.RawMessage(`{
		"setup_sqls":["SET enable_profile = true"],
		"sql":"SELECT 1"
	}`))
	require.NoError(t, err)
	assert.Contains(t, result.Text, "(blocked:")
}

func TestPingTool_UsesWorkspaceDotenvPerCall(t *testing.T) {
	clearDorisEnv(t)

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	writeWorkspaceDotenv(t, workspaceA, "DORIS_FE_HOST=fe-a.example\nDORIS_FE_PORT=9131\nDORIS_USER=user_a\nDORIS_DATABASE=db_a\n")
	writeWorkspaceDotenv(t, workspaceB, "DORIS_FE_HOST=fe-b.example\nDORIS_FE_PORT=9132\nDORIS_USER=user_b\nDORIS_DATABASE=db_b\n")

	var seen []Config
	handler := newPingTool(func(cfg Config) (Conn, error) {
		seen = append(seen, cfg)
		return &fakeConn{
			pingInfo: PingInfo{
				User:     cfg.User,
				Database: cfg.Database,
				Version:  "2.1.8",
			},
		}, nil
	})

	resultA, err := handler.Handler(context.Background(), &tool.Context{Workspace: workspaceA}, json.RawMessage(`{}`))
	require.NoError(t, err)
	resultB, err := handler.Handler(context.Background(), &tool.Context{Workspace: workspaceB}, json.RawMessage(`{}`))
	require.NoError(t, err)

	require.Len(t, seen, 2)
	assert.Equal(t, "fe-a.example", seen[0].Host)
	assert.Equal(t, 9131, seen[0].Port)
	assert.Equal(t, "user_a", seen[0].User)
	assert.Equal(t, "db_a", seen[0].Database)
	assert.Equal(t, "fe-b.example", seen[1].Host)
	assert.Equal(t, 9132, seen[1].Port)
	assert.Equal(t, "user_b", seen[1].User)
	assert.Equal(t, "db_b", seen[1].Database)
	assert.Contains(t, resultA.Text, "OK: connected to fe-a.example:9131")
	assert.Contains(t, resultB.Text, "OK: connected to fe-b.example:9132")

	_, exists := os.LookupEnv("DORIS_FE_HOST")
	assert.False(t, exists, "workspace .env must not leak into process env")
}

func TestPingTool_ProcessEnvOverridesWorkspaceDotenv(t *testing.T) {
	clearDorisEnv(t)

	workspace := t.TempDir()
	writeWorkspaceDotenv(t, workspace, "DORIS_FE_HOST=fe-file.example\nDORIS_FE_PORT=9133\n")
	t.Setenv("DORIS_FE_HOST", "fe-env.example")
	t.Setenv("DORIS_FE_PORT", "9233")

	handler := newPingTool(func(cfg Config) (Conn, error) {
		assert.Equal(t, "fe-env.example", cfg.Host)
		assert.Equal(t, 9233, cfg.Port)
		return &fakeConn{
			pingInfo: PingInfo{
				User:     cfg.User,
				Database: cfg.Database,
				Version:  "2.1.8",
			},
		}, nil
	})

	_, err := handler.Handler(context.Background(), &tool.Context{Workspace: workspace}, json.RawMessage(`{}`))
	require.NoError(t, err)
}

func clearDorisEnv(t *testing.T) {
	t.Helper()
	for _, pair := range os.Environ() {
		key, _, ok := cutString(pair, "=")
		if ok && len(key) > 6 && key[:6] == "DORIS_" {
			t.Setenv(key, "")
			require.NoError(t, os.Unsetenv(key))
		}
	}
}

func writeWorkspaceDotenv(t *testing.T, workspace, content string) {
	t.Helper()
	path := filepath.Join(workspace, ".env")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func cutString(s, sep string) (string, string, bool) {
	i := 0
	for i < len(s) {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
		i++
	}
	return s, "", false
}
