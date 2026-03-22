package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/config"
)

func TestParseModelString_WithProvider(t *testing.T) {
	provider, model := ParseModelString("openrouter:qwen/qwen3-coder-next")
	assert.Equal(t, "openrouter", provider)
	assert.Equal(t, "qwen/qwen3-coder-next", model)
}

func TestParseModelString_WithoutProvider(t *testing.T) {
	provider, model := ParseModelString("gpt-4o")
	assert.Equal(t, "", provider)
	assert.Equal(t, "gpt-4o", model)
}

func TestParseModelString_OpenAI(t *testing.T) {
	provider, model := ParseModelString("openai:gpt-4o-mini")
	assert.Equal(t, "openai", provider)
	assert.Equal(t, "gpt-4o-mini", model)
}

func TestResponse_IsToolCall_WithToolCalls(t *testing.T) {
	r := &Response{
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "bash",
			Arguments: json.RawMessage(`{"cmd":"echo hello"}`),
		}},
	}
	assert.True(t, r.IsToolCall())
}

func TestResponse_IsToolCall_TextOnly(t *testing.T) {
	r := &Response{
		Text: "Hello!",
	}
	assert.False(t, r.IsToolCall())
}

func TestResponse_IsToolCall_Empty(t *testing.T) {
	r := &Response{}
	assert.False(t, r.IsToolCall())
}

func TestInput_IsMultimodal(t *testing.T) {
	plain := Input{Text: "hello"}
	assert.False(t, plain.IsMultimodal())

	multi := Input{
		Parts: []ContentPart{
			{Type: "text", Text: "describe this image"},
			{Type: "image_url", ImageURL: &ImageURLPart{URL: "data:image/png;base64,abc"}},
		},
	}
	assert.True(t, multi.IsMultimodal())
}

func TestNewProviderRouter_BaseOnlyProvider(t *testing.T) {
	// Scenario: ORI_API_KEY=sk-global + ORI_OPENROUTER_API_BASE=https://openrouter.ai/api/v1
	// Before fix: openrouter client would NOT be created.
	// After fix:  openrouter client SHOULD be created with globalKey + openrouter base.
	cfg := &config.Settings{
		Model:     "openrouter:qwen/qwen3-coder-next",
		APIFormat: config.APIFormatCompletion,
		APIKey: map[string]string{
			"": "sk-global",
		},
		APIBase: map[string]string{
			"":           "https://api.default.com/v1",
			"openrouter": "https://openrouter.ai/api/v1",
		},
	}

	router, err := NewProviderRouter(cfg)
	require.NoError(t, err)

	// The openrouter provider should have been discovered and created.
	assert.NotNil(t, router.clients["openrouter"], "openrouter client should be created for base-only provider")

	// The default client should also exist (from global key).
	assert.NotNil(t, router.defaultClient, "default client should be created from global key")
}

func TestNewProviderRouter_KeyOnlyProvider(t *testing.T) {
	// Scenario: only ORI_OPENAI_API_KEY is set, no base URL.
	// Should create openai client with the key and no custom base.
	cfg := &config.Settings{
		Model:     "openai:gpt-4o",
		APIFormat: config.APIFormatCompletion,
		APIKey: map[string]string{
			"openai": "sk-openai",
		},
		APIBase: map[string]string{},
	}

	router, err := NewProviderRouter(cfg)
	require.NoError(t, err)
	assert.NotNil(t, router.clients["openai"], "openai client should be created for key-only provider")
}

func TestNewProviderRouter_DefaultModelProviderFromGlobalCredentials(t *testing.T) {
	cfg := &config.Settings{
		Model:     "openai:gpt-5.4",
		APIFormat: config.APIFormatCompletion,
		APIKey: map[string]string{
			"": "sk-global",
		},
		APIBase: map[string]string{
			"": "https://api.example.com/v1",
		},
	}

	router, err := NewProviderRouter(cfg)
	require.NoError(t, err)
	assert.NotNil(t, router.clients["openai"], "default model provider should be created from global credentials")
	assert.NotNil(t, router.defaultClient, "default client should still exist for unprefixed models")
}

func TestNewProviderRouter_FallbackProviderFromGlobalCredentials(t *testing.T) {
	cfg := &config.Settings{
		Model:          "gpt-5.4",
		FallbackModels: []string{"openrouter:qwen/qwen3-coder-next"},
		APIFormat:      config.APIFormatCompletion,
		APIKey: map[string]string{
			"": "sk-global",
		},
		APIBase: map[string]string{
			"": "https://api.example.com/v1",
		},
	}

	router, err := NewProviderRouter(cfg)
	require.NoError(t, err)
	assert.NotNil(t, router.clients["openrouter"], "fallback provider should be created from global credentials")
}

func TestNewProviderRouter_NoKeyAnywhere(t *testing.T) {
	// Scenario: ORI_OPENROUTER_API_BASE is set but no key anywhere.
	// Should skip the provider (warn) and fail since no client is available.
	cfg := &config.Settings{
		Model:     "openrouter:qwen/qwen3-coder-next",
		APIFormat: config.APIFormatCompletion,
		APIKey:    map[string]string{},
		APIBase: map[string]string{
			"openrouter": "https://openrouter.ai/api/v1",
		},
	}

	_, err := NewProviderRouter(cfg)
	assert.Error(t, err, "should fail when no API key is available anywhere")
}

func TestNewProviderRouter_MultiProviderMixed(t *testing.T) {
	// Scenario: openai has its own key, openrouter has only a base.
	// Both should be created; openrouter inherits the global key.
	cfg := &config.Settings{
		Model:     "openrouter:qwen/qwen3-coder-next",
		APIFormat: config.APIFormatCompletion,
		APIKey: map[string]string{
			"":       "sk-global",
			"openai": "sk-openai",
		},
		APIBase: map[string]string{
			"openrouter": "https://openrouter.ai/api/v1",
		},
	}

	router, err := NewProviderRouter(cfg)
	require.NoError(t, err)
	assert.NotNil(t, router.clients["openai"])
	assert.NotNil(t, router.clients["openrouter"])
	assert.NotNil(t, router.defaultClient)
}

func TestNewProviderRouter_NoPrefixModelWithoutGlobalKey_SingleProvider(t *testing.T) {
	// Scenario: ORI_OPENAI_API_KEY=sk-xxx + ORI_MODEL=gpt-5.4 (no prefix)
	// Single provider auto-promote: should succeed because only one provider is available.
	cfg := &config.Settings{
		Model:     "gpt-5.4",
		APIFormat: config.APIFormatCompletion,
		APIKey: map[string]string{
			"openai": "sk-openai",
		},
		APIBase: map[string]string{},
	}

	router, err := NewProviderRouter(cfg)
	require.NoError(t, err, "single provider should be auto-promoted as default")
	assert.NotNil(t, router.defaultClient, "defaultClient should be auto-promoted from the single provider")
}

func TestNewProviderRouter_NoPrefixModelWithoutGlobalKey_MultiProvider(t *testing.T) {
	// Scenario: ORI_OPENAI_API_KEY + ORI_ANTHROPIC_API_KEY + ORI_MODEL=gpt-5.4 (no prefix)
	// Cannot auto-promote because there are multiple providers.
	cfg := &config.Settings{
		Model:     "gpt-5.4",
		APIFormat: config.APIFormatCompletion,
		APIKey: map[string]string{
			"openai":    "sk-openai",
			"anthropic": "sk-anthropic",
		},
		APIBase: map[string]string{},
	}

	_, err := NewProviderRouter(cfg)
	assert.Error(t, err, "should fail when model has no prefix and multiple providers exist")
	assert.Contains(t, err.Error(), "no provider prefix")
}
