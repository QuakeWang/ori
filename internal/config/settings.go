package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuakeWang/ori/internal/envutil"
)

// APIFormat represents the wire protocol format for LLM requests.
type APIFormat string

const (
	APIFormatCompletion APIFormat = "completion"
	APIFormatResponses  APIFormat = "responses"
)

// validAPIFormats enumerates the accepted ORI_API_FORMAT values.
var validAPIFormats = map[APIFormat]bool{
	APIFormatCompletion: true,
	APIFormatResponses:  true,
}

const (
	DefaultModel     = "openrouter:qwen/qwen3-coder-next"
	DefaultAPIFormat = APIFormatCompletion
	DefaultMaxSteps  = 50
	DefaultMaxToken  = 16384
)

// Settings holds runtime configuration loaded from environment variables
// and .env files. All keys use the ORI_ prefix.
type Settings struct {
	Home           string
	Model          string
	FallbackModels []string
	APIKey         map[string]string // provider -> key
	APIBase        map[string]string // provider -> base URL
	APIFormat      APIFormat         // APIFormatCompletion | APIFormatResponses
	MaxSteps       int
	MaxTokens      int
	ModelTimeout   time.Duration
	Verbose        int
}

// Load reads configuration from the workspace .env (if present) merged with the
// current process environment, without mutating os.Environ().
// When workspace is non-empty, it reads workspace/.env; otherwise it falls back
// to ".env" relative to the process working directory.
// Existing process env values win over .env values.
// Multi-provider keys are detected via ORI_{PROVIDER}_API_KEY / ORI_{PROVIDER}_API_BASE patterns.
func Load(workspace string) (*Settings, error) {
	env, err := envutil.Snapshot(workspace)
	if err != nil {
		return nil, err
	}

	apiFormat := APIFormat(envutil.StringOr(env, "ORI_API_FORMAT", string(DefaultAPIFormat)))
	if !validAPIFormats[apiFormat] {
		return nil, fmt.Errorf("invalid ORI_API_FORMAT %q: must be one of completion, responses", apiFormat)
	}

	maxSteps, err := envutil.IntOr(env, "ORI_MAX_STEPS", DefaultMaxSteps)
	if err != nil {
		return nil, err
	}
	if maxSteps <= 0 {
		return nil, fmt.Errorf("invalid ORI_MAX_STEPS %d: must be > 0", maxSteps)
	}

	maxTokens, err := envutil.IntOr(env, "ORI_MAX_TOKENS", DefaultMaxToken)
	if err != nil {
		return nil, err
	}
	if maxTokens <= 0 {
		return nil, fmt.Errorf("invalid ORI_MAX_TOKENS %d: must be > 0", maxTokens)
	}

	verbose, err := envutil.IntOr(env, "ORI_VERBOSE", 0)
	if err != nil {
		return nil, err
	}
	if verbose < 0 || verbose > 2 {
		return nil, fmt.Errorf("invalid ORI_VERBOSE %d: must be in range [0, 2]", verbose)
	}

	s := &Settings{
		Home:      resolveHome(envutil.StringOr(env, "ORI_HOME", defaultHome())),
		Model:     envutil.StringOr(env, "ORI_MODEL", DefaultModel),
		APIFormat: apiFormat,
		MaxSteps:  maxSteps,
		MaxTokens: maxTokens,
		Verbose:   verbose,
	}

	if v := env["ORI_MODEL_TIMEOUT_SECONDS"]; v != "" {
		seconds, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid ORI_MODEL_TIMEOUT_SECONDS: %w", err)
		}
		s.ModelTimeout = time.Duration(seconds) * time.Second
	}

	if v := env["ORI_FALLBACK_MODELS"]; v != "" {
		for _, m := range strings.Split(v, ",") {
			if t := strings.TrimSpace(m); t != "" {
				s.FallbackModels = append(s.FallbackModels, t)
			}
		}
	}

	s.APIKey, s.APIBase = resolveProviderCredentials(env)
	return s, nil
}

// ------------------------------------------------------------------ helpers

var (
	keyRE  = regexp.MustCompile(`^ORI_([A-Z0-9]+)_API_KEY$`)
	baseRE = regexp.MustCompile(`^ORI_([A-Z0-9]+)_API_BASE$`)
)

// resolveProviderCredentials scans env for per-provider or global credentials.
// If a global ORI_API_KEY + ORI_API_BASE pair exists, it is stored under the
// empty-string key ("") in the returned maps.
func resolveProviderCredentials(env map[string]string) (keys, bases map[string]string) {
	keys = make(map[string]string)
	bases = make(map[string]string)

	// Global fallback.
	if v := env["ORI_API_KEY"]; v != "" {
		keys[""] = v
	}
	if v := env["ORI_API_BASE"]; v != "" {
		bases[""] = v
	}

	// Per-provider overrides.
	for k, v := range env {
		if m := keyRE.FindStringSubmatch(k); m != nil {
			keys[strings.ToLower(m[1])] = v
		}
		if m := baseRE.FindStringSubmatch(k); m != nil {
			bases[strings.ToLower(m[1])] = v
		}
	}
	return keys, bases
}

func defaultHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".ori")
}

func resolveHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}
