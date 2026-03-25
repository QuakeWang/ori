package doris

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/QuakeWang/ori/internal/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileTool_DefaultViewReturnsMergedProfile(t *testing.T) {
	clearDorisEnv(t)

	profileText := sampleProfileText()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/profile/text", r.URL.Path)
		assert.Equal(t, "abc-123", r.URL.Query().Get("query_id"))
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "root", user)
		assert.Equal(t, "secret", pass)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(profileText))
	}))
	defer ts.Close()

	workspace := writeProfileWorkspace(t, ts.URL)
	profileTool := newProfileTool()

	result, err := profileTool.Handler(
		context.Background(),
		&tool.Context{Workspace: workspace},
		json.RawMessage(`{"query_id":"abc-123"}`),
	)
	require.NoError(t, err)

	assert.Contains(t, result.Text, "Summary:")
	assert.Contains(t, result.Text, "Execution  Summary:")
	assert.Contains(t, result.Text, "Changed  Session  Variables:")
	assert.Contains(t, result.Text, "Physical  Plan")
	assert.Contains(t, result.Text, "MergedProfile:")
	assert.Contains(t, result.Text, "VOLAP_SCAN: ExecTime=12s")
	assert.NotContains(t, result.Text, "Execution Profile abc-123:")
	assert.NotContains(t, result.Text, "PipelineTask")
	assert.False(t, result.Truncated)
	assert.Equal(t, "merged", result.Meta["view"])
	assert.Equal(t, false, result.Meta["source_truncated"])
}

func TestProfileTool_DetailViewReturnsDetailProfile(t *testing.T) {
	clearDorisEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(sampleProfileText()))
	}))
	defer ts.Close()

	workspace := writeProfileWorkspace(t, ts.URL)
	profileTool := newProfileTool()

	result, err := profileTool.Handler(
		context.Background(),
		&tool.Context{Workspace: workspace},
		json.RawMessage(`{"query_id":"abc-123","view":"detail"}`),
	)
	require.NoError(t, err)

	assert.Contains(t, result.Text, "Execution Profile abc-123:")
	assert.Contains(t, result.Text, "PipelineTask (index=0)")
	assert.NotContains(t, result.Text, "Summary:")
	assert.NotContains(t, result.Text, "MergedProfile:")
	assert.Equal(t, "detail", result.Meta["view"])
}

func TestProfileTool_BriefJSONViewUsesManagerAPI(t *testing.T) {
	clearDorisEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rest/v2/manager/query/profile/json/abc-123", r.URL.Path)
		assert.Equal(t, "is_all_node=false", r.URL.RawQuery)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "success",
			"data": map[string]any{
				"profile": `{"name":"root","children":[{"name":"scan","exec_time":"12s"}]}`,
			},
		})
	}))
	defer ts.Close()

	workspace := writeProfileWorkspace(t, ts.URL)
	profileTool := newProfileTool()

	result, err := profileTool.Handler(
		context.Background(),
		&tool.Context{Workspace: workspace},
		json.RawMessage(`{"query_id":"abc-123","view":"brief_json"}`),
	)
	require.NoError(t, err)

	assert.Contains(t, result.Text, `"name": "root"`)
	assert.Contains(t, result.Text, `"exec_time": "12s"`)
	assert.Equal(t, "brief_json", result.Meta["view"])
}

func TestProfileTool_MissingQueryID(t *testing.T) {
	profileTool := newProfileTool()

	_, err := profileTool.Handler(
		context.Background(),
		&tool.Context{Workspace: t.TempDir()},
		json.RawMessage(`{"query_id":""}`),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query_id is required")
}

func TestResolveProfileView_Invalid(t *testing.T) {
	_, err := resolveProfileView("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid view")
}

func TestFetchProfile_PlainTextResponse(t *testing.T) {
	expected := "Summary:\n  Total: 10s"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/profile/text", r.URL.Path)
		assert.Equal(t, "test-query-id", r.URL.Query().Get("query_id"))
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(expected))
	}))
	defer ts.Close()

	cfg := profileConfigFromServer(t, ts.URL)
	result, err := fetchProfile(context.Background(), cfg, "test-query-id")
	require.NoError(t, err)
	assert.Equal(t, expected, result.Text)
	assert.False(t, result.Truncated)
}

func TestFetchProfile_ProfileNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("query id test-id not found"))
	}))
	defer ts.Close()

	cfg := profileConfigFromServer(t, ts.URL)
	_, err := fetchProfile(context.Background(), cfg, "test-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile not found")
}

func TestFetchProfile_Truncation(t *testing.T) {
	bigProfile := make([]byte, maxProfileBytes+1000)
	for i := range bigProfile {
		bigProfile[i] = 'x'
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(bigProfile)
	}))
	defer ts.Close()

	cfg := profileConfigFromServer(t, ts.URL)
	result, err := fetchProfile(context.Background(), cfg, "big-query")
	require.NoError(t, err)
	assert.Len(t, result.Text, maxProfileBytes)
	assert.True(t, result.Truncated)
}

func TestSplitProfileSections(t *testing.T) {
	sections := splitProfileSections(sampleProfileText())

	assert.Contains(t, sections.Summary, "Summary:")
	assert.Contains(t, sections.ExecutionSummary, "Execution  Summary:")
	assert.Contains(t, sections.ChangedSessionVariables, "Changed  Session  Variables:")
	assert.Contains(t, sections.PhysicalPlan, "Physical  Plan")
	assert.Contains(t, sections.MergedProfile, "MergedProfile:")
	assert.Contains(t, sections.DetailProfile, "Execution Profile abc-123:")
}

func TestRenderProfileView_Fallbacks(t *testing.T) {
	noMerged := strings.Join([]string{
		"Summary:",
		"  - Total: 2s",
		"Execution Summary:",
		"  - Plan Time: 1ms",
		"Execution Profile abc-123:",
		"  Fragment 0:",
		"    PipelineTask (index=0):(ExecTime: 2s)",
	}, "\n")
	assert.Contains(t, renderProfileView(noMerged, profileViewMerged), "MergedProfile unavailable")
	assert.Contains(t, renderProfileView(noMerged, profileViewMerged), "PipelineTask")

	noDetail := strings.Join([]string{
		"Summary:",
		"  - Total: 2s",
		"MergedProfile:",
		"  Fragment 0:",
		"    VOLAP_SCAN: ExecTime=2s",
	}, "\n")
	assert.Contains(t, renderProfileView(noDetail, profileViewDetail), "DetailProfile unavailable")
	assert.Contains(t, renderProfileView(noDetail, profileViewDetail), "VOLAP_SCAN: ExecTime=2s")
}

func sampleProfileText() string {
	return strings.Join([]string{
		"Summary:",
		"  - Profile ID: abc-123",
		"  - Total: 40sec481ms",
		"  - Task State: OK",
		"Execution  Summary:",
		"  - Plan Time: 11ms",
		"  - Schedule Time: 8ms",
		"Changed  Session  Variables:",
		"  enable_profile | true | true",
		"Physical  Plan",
		"PhysicalResultSink[1]",
		"  +--PhysicalHashAggregate[2]",
		"MergedProfile:",
		"  Fragment 0:",
		"    VAGGREGATE: ExecTime=15s, InputRows=1000000",
		"    VOLAP_SCAN: ExecTime=12s, InputRows=5000000",
		"Execution Profile abc-123:",
		"  Fragment 0:",
		"    Pipeline :0 (host=10.0.0.1):",
		"      PipelineTask (index=0):(ExecTime: 39s)",
		"        - TaskState: Finished",
	}, "\n")
}

func writeProfileWorkspace(t *testing.T, serverURL string) string {
	t.Helper()

	workspace := t.TempDir()
	cfg := profileConfigFromServer(t, serverURL)
	writeWorkspaceDotenv(t, workspace, fmt.Sprintf(
		"DORIS_FE_HOST=%s\nDORIS_FE_HTTP_PORT=%d\nDORIS_USER=root\nDORIS_PASSWORD=secret\n",
		cfg.Host, cfg.HTTPPort,
	))
	return workspace
}

func profileConfigFromServer(t *testing.T, serverURL string) Config {
	t.Helper()

	u, err := url.Parse(serverURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return Config{
		Host:     u.Hostname(),
		HTTPPort: port,
		User:     "root",
		Password: "secret",
	}
}
