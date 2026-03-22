package doris

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/QuakeWang/ori/internal/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileTool_Success(t *testing.T) {
	clearDorisEnv(t)

	profileText := "Summary:\n  Total: 40sec481ms\n  Task State: OK\n\nExecution Profile:\n  Fragment 0:\n    VAGGREGATE: ExecTime=15s\n    VHASH_JOIN: ExecTime=10s\n    VOLAP_SCAN (lineitem): ExecTime=12s"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rest/v1/query_profile/abc-123", r.URL.Path)
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "root", user)
		assert.Equal(t, "secret", pass)
		resp := map[string]any{
			"code": 0,
			"msg":  "success",
			"data": profileText,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	workspace := t.TempDir()
	writeWorkspaceDotenv(t, workspace, fmt.Sprintf(
		"DORIS_FE_HOST=%s\nDORIS_FE_HTTP_PORT=%d\nDORIS_USER=root\nDORIS_PASSWORD=secret\n",
		u.Hostname(), port,
	))

	profileTool := newProfileTool()
	result, err := profileTool.Handler(
		context.Background(),
		&tool.Context{Workspace: workspace},
		json.RawMessage(`{"query_id":"abc-123"}`),
	)
	require.NoError(t, err)
	assert.Equal(t, profileText, result.Text)
	assert.False(t, result.Truncated)
}

func TestProfileTool_MissingQueryID(t *testing.T) {
	profileTool := newProfileTool()
	tc := &tool.Context{Workspace: t.TempDir()}

	// Create a minimal .env for config loading.
	// We don't need to actually connect.
	input := json.RawMessage(`{"query_id":""}`)
	_, err := profileTool.Handler(context.Background(), tc, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query_id is required")
}

func TestFetchProfile_ParsesJSONResponse(t *testing.T) {
	expected := "Fragment 0: ExecTime=10s"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code": 0,
			"msg":  "success",
			"data": expected,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	// Parse the test server URL.
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	cfg := Config{
		Host:     host,
		HTTPPort: port,
		User:     "testuser",
		Password: "testpass",
	}

	result, err := fetchProfile(context.Background(), cfg, "test-query-id")
	require.NoError(t, err)
	assert.Equal(t, expected, result)
}

func TestFetchProfile_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code": -1,
			"msg":  "ID test-id does not exist",
			"data": "",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	cfg := Config{Host: host, HTTPPort: port, User: "root", Password: ""}

	_, err = fetchProfile(context.Background(), cfg, "test-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestFetchProfile_Truncation(t *testing.T) {
	// Generate a profile larger than maxProfileBytes.
	bigProfile := make([]byte, maxProfileBytes+1000)
	for i := range bigProfile {
		bigProfile[i] = 'x'
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code": 0,
			"msg":  "success",
			"data": string(bigProfile),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	cfg := Config{Host: host, HTTPPort: port, User: "root", Password: ""}

	result, err := fetchProfile(context.Background(), cfg, "big-query")
	require.NoError(t, err)
	assert.Len(t, result, maxProfileBytes)
}
