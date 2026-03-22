package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_Defaults(t *testing.T) {
	clearOriEnv(t)

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, DefaultModel, cfg.Model)
	assert.Equal(t, DefaultAPIFormat, cfg.APIFormat)
	assert.Equal(t, DefaultMaxSteps, cfg.MaxSteps)
	assert.Equal(t, DefaultMaxToken, cfg.MaxTokens)
	assert.Equal(t, 0, cfg.Verbose)
	assert.Contains(t, cfg.Home, ".ori")
}

func TestLoad_OverrideViaEnv(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_MODEL", "openai:gpt-4o")
	t.Setenv("ORI_API_FORMAT", "responses")
	t.Setenv("ORI_MAX_STEPS", "100")
	t.Setenv("ORI_MAX_TOKENS", "4096")
	t.Setenv("ORI_VERBOSE", "2")
	t.Setenv("ORI_MODEL_TIMEOUT_SECONDS", "60")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "openai:gpt-4o", cfg.Model)
	assert.Equal(t, APIFormatResponses, cfg.APIFormat)
	assert.Equal(t, 100, cfg.MaxSteps)
	assert.Equal(t, 4096, cfg.MaxTokens)
	assert.Equal(t, 2, cfg.Verbose)
	assert.Equal(t, 60_000_000_000, int(cfg.ModelTimeout)) // 60s in nanoseconds
}

func TestLoad_GlobalAPICredentials(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_API_KEY", "sk-global")
	t.Setenv("ORI_API_BASE", "https://api.example.com/v1")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "sk-global", cfg.APIKey[""])
	assert.Equal(t, "https://api.example.com/v1", cfg.APIBase[""])
}

func TestLoad_MultiProviderCredentials(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_OPENAI_API_KEY", "sk-openai")
	t.Setenv("ORI_OPENAI_API_BASE", "https://api.openai.com/v1")
	t.Setenv("ORI_OPENROUTER_API_KEY", "sk-or")
	t.Setenv("ORI_OPENROUTER_API_BASE", "https://openrouter.ai/api/v1")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "sk-openai", cfg.APIKey["openai"])
	assert.Equal(t, "https://api.openai.com/v1", cfg.APIBase["openai"])
	assert.Equal(t, "sk-or", cfg.APIKey["openrouter"])
	assert.Equal(t, "https://openrouter.ai/api/v1", cfg.APIBase["openrouter"])
}

func TestLoad_FallbackModels(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_FALLBACK_MODELS", "openai:gpt-4o, anthropic:claude-sonnet")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, []string{"openai:gpt-4o", "anthropic:claude-sonnet"}, cfg.FallbackModels)
}

func TestLoad_InvalidTimeout(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_MODEL_TIMEOUT_SECONDS", "not-a-number")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ORI_MODEL_TIMEOUT_SECONDS")
}

func TestLoad_APIFormatDefault(t *testing.T) {
	clearOriEnv(t)

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, APIFormatCompletion, cfg.APIFormat)
}

func TestLoad_InvalidAPIFormat(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_API_FORMAT", "invalid")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ORI_API_FORMAT")
}

func TestLoad_APIFormatUnsupportedValueRejected(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_API_FORMAT", "messages")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "completion, responses")
}

func TestLoad_InvalidMaxSteps_NotANumber(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_MAX_STEPS", "abc")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ORI_MAX_STEPS")
}

func TestLoad_InvalidMaxSteps_Zero(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_MAX_STEPS", "0")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ORI_MAX_STEPS")
}

func TestLoad_InvalidMaxTokens_Negative(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_MAX_TOKENS", "-1")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ORI_MAX_TOKENS")
}

func TestLoad_InvalidVerbose_OutOfRange(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_VERBOSE", "5")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ORI_VERBOSE")
}

func TestLoad_InvalidVerbose_NotANumber(t *testing.T) {
	clearOriEnv(t)
	t.Setenv("ORI_VERBOSE", "hello")

	_, err := Load("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ORI_VERBOSE")
}

func TestLoad_WorkspaceDotenvIsScopedPerCall(t *testing.T) {
	clearOriEnv(t)

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	writeDotenv(t, workspaceA, "ORI_MODEL=openai:gpt-a\nORI_VERBOSE=1\n")
	writeDotenv(t, workspaceB, "ORI_MODEL=openai:gpt-b\nORI_VERBOSE=2\n")

	cfgA, err := Load(workspaceA)
	require.NoError(t, err)
	cfgB, err := Load(workspaceB)
	require.NoError(t, err)

	assert.Equal(t, "openai:gpt-a", cfgA.Model)
	assert.Equal(t, 1, cfgA.Verbose)
	assert.Equal(t, "openai:gpt-b", cfgB.Model)
	assert.Equal(t, 2, cfgB.Verbose)

	_, exists := os.LookupEnv("ORI_MODEL")
	assert.False(t, exists, "workspace .env must not leak into process env")
}

func TestLoad_ProcessEnvOverridesWorkspaceDotenv(t *testing.T) {
	clearOriEnv(t)
	workspace := t.TempDir()
	writeDotenv(t, workspace, "ORI_MODEL=openai:gpt-file\n")
	t.Setenv("ORI_MODEL", "openai:gpt-env")

	cfg, err := Load(workspace)
	require.NoError(t, err)

	assert.Equal(t, "openai:gpt-env", cfg.Model)
}

func TestLoad_ExpandsTildeInHome(t *testing.T) {
	clearOriEnv(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	t.Setenv("ORI_HOME", "~/.ori-custom")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(home, ".ori-custom"), cfg.Home)
}

// clearOriEnv unsets all ORI_ prefixed environment variables for test isolation.
func clearOriEnv(t *testing.T) {
	t.Helper()
	for _, pair := range os.Environ() {
		k, _, _ := cutString(pair, "=")
		if len(k) > 4 && k[:4] == "ORI_" {
			t.Setenv(k, "")
			require.NoError(t, os.Unsetenv(k))
		}
	}
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

func writeDotenv(t *testing.T, workspace, content string) {
	t.Helper()
	path := filepath.Join(workspace, ".env")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
