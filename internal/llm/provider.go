package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/QuakeWang/ori/internal/config"
)

// OpenRouter-specific headers.
var openRouterHeaders = map[string]string{
	"X-Title": "Ori",
}

// ProviderRouter implements Client by routing to per-provider OpenAI-compatible
// clients based on the model prefix (e.g. "openrouter:qwen/qwen3-coder-next").
// It also handles fallback model retry when configured.
type ProviderRouter struct {
	clients        map[string]Client // provider name -> client
	defaultClient  Client            // fallback when no provider prefix
	fallbackModels []string          // "provider:model" strings to try on error
}

// NewProviderRouter creates a ProviderRouter from Settings.
// It creates one OpenAIClient per discovered provider.
func NewProviderRouter(cfg *config.Settings) (*ProviderRouter, error) {
	r := &ProviderRouter{
		clients:        make(map[string]Client),
		fallbackModels: cfg.FallbackModels,
	}

	// Collect providers from both credential maps and configured model strings.
	// A provider is routable if it is explicitly configured, or referenced by the
	// primary/fallback models; the missing key/base inherits from global.
	allProviders := make(map[string]bool)
	for provider := range cfg.APIKey {
		if provider != "" {
			allProviders[provider] = true
		}
	}
	for provider := range cfg.APIBase {
		if provider != "" {
			allProviders[provider] = true
		}
	}
	if provider, _ := ParseModelString(cfg.Model); provider != "" {
		allProviders[provider] = true
	}
	for _, model := range cfg.FallbackModels {
		if provider, _ := ParseModelString(model); provider != "" {
			allProviders[provider] = true
		}
	}

	globalKey := cfg.APIKey[""]
	globalBase := cfg.APIBase[""]

	for provider := range allProviders {
		key := cfg.APIKey[provider]
		base := cfg.APIBase[provider]

		// Symmetric fallback: both key and base inherit from global if missing.
		if key == "" {
			key = globalKey
		}
		if base == "" {
			base = globalBase
		}

		// Still need at least a key to actually call the API.
		if key == "" {
			slog.Warn("llm.provider.skipped", "provider", provider, "reason", "no API key (provider-specific or global)")
			continue
		}

		headers := map[string]string{}
		if provider == "openrouter" {
			headers = openRouterHeaders
		}

		r.clients[provider] = NewOpenAIClient(key, base, headers, cfg.APIFormat)
		slog.Info("llm.provider.registered", "provider", provider, "base", base)
	}

	// Global client as default fallback.
	if globalKey != "" {
		headers := map[string]string{}
		if strings.HasPrefix(cfg.Model, "openrouter:") {
			headers = openRouterHeaders
		}
		r.defaultClient = NewOpenAIClient(globalKey, globalBase, headers, cfg.APIFormat)
		slog.Info("llm.provider.default", "base", globalBase)
	}

	// Single-provider auto-promote: when no global ORI_API_KEY is set but
	// exactly one provider is configured, use it as the default client.
	// This makes ORI_OPENAI_API_KEY=... ORI_MODEL=gpt-5.4 "just work".
	if r.defaultClient == nil && len(r.clients) == 1 {
		for name, c := range r.clients {
			r.defaultClient = c
			slog.Info("llm.provider.auto_default", "provider", name)
		}
	}

	if len(r.clients) == 0 && r.defaultClient == nil {
		return nil, fmt.Errorf("no LLM provider configured: set ORI_API_KEY or ORI_{PROVIDER}_API_KEY")
	}

	// Validate that the primary model (and each fallback) can be routed.
	// Fail early instead of surfacing a confusing error on the first LLM call.
	for _, model := range append([]string{cfg.Model}, cfg.FallbackModels...) {
		provider, _ := ParseModelString(model)
		client := r.resolveClient(provider)
		if client == nil {
			if provider == "" {
				return nil, fmt.Errorf(
					"model %q has no provider prefix and no global ORI_API_KEY is set; "+
						"either use 'provider:model' format (e.g. 'openai:%s') or set ORI_API_KEY",
					model, model,
				)
			}
			return nil, fmt.Errorf(
				"model %q references provider %q but no API key is available for it; "+
					"set ORI_%s_API_KEY or ORI_API_KEY",
				model, provider, strings.ToUpper(provider),
			)
		}
	}

	return r, nil
}

// Chat routes the request to the appropriate provider client based on model prefix.
// If the primary model fails and FallbackModels are configured, it retries in order.
func (r *ProviderRouter) Chat(ctx context.Context, req Request) (*Response, error) {
	resp, err := r.chatOnce(ctx, req)
	if err == nil {
		return resp, nil
	}

	// Try fallback models in order.
	primaryErr := err
	for _, fallbackModel := range r.fallbackModels {
		slog.Warn("llm.fallback", "from", req.Model, "to", fallbackModel, "reason", primaryErr.Error())
		fallbackReq := req
		fallbackReq.Model = fallbackModel
		resp, err = r.chatOnce(ctx, fallbackReq)
		if err == nil {
			return resp, nil
		}
		slog.Warn("llm.fallback.failed", "model", fallbackModel, "error", err)
	}

	// All attempts failed; return the original error.
	return nil, primaryErr
}

func (r *ProviderRouter) chatOnce(ctx context.Context, req Request) (*Response, error) {
	provider, model := ParseModelString(req.Model)

	client := r.resolveClient(provider)
	if client == nil {
		// Build a helpful error for runtime model overrides (opts.Model, subagent).
		if provider == "" {
			return nil, fmt.Errorf(
				"no client for model %q: use 'provider:model' format or set ORI_API_KEY",
				req.Model,
			)
		}
		return nil, fmt.Errorf(
			"no client for provider %q (model: %s): set ORI_%s_API_KEY or ORI_API_KEY",
			provider, req.Model, strings.ToUpper(provider),
		)
	}

	routed := req
	routed.Model = model

	return client.Chat(ctx, routed)
}

// resolveClient returns the client for the given provider name.
// When provider is empty (i.e. the model string had no "provider:" prefix),
// it returns the defaultClient. The defaultClient is either the explicit
// global-key client, or the single auto-promoted provider client.
func (r *ProviderRouter) resolveClient(provider string) Client {
	if provider == "" {
		return r.defaultClient
	}
	return r.clients[provider]
}

// ParseModelString splits "provider:model" into (provider, model).
// "gpt-4o" returns ("", "gpt-4o").
// "openrouter:qwen/qwen3-coder-next" returns ("openrouter", "qwen/qwen3-coder-next").
func ParseModelString(s string) (provider, model string) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", s
}
