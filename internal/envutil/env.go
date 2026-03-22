package envutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Snapshot returns a per-call view of configuration composed from the
// workspace .env file and the current process environment, without mutating
// os.Environ(). Existing process env values override .env values.
func Snapshot(workspace string) (map[string]string, error) {
	dotenv, err := readDotenv(dotenvPath(workspace))
	if err != nil {
		return nil, err
	}

	env := make(map[string]string, len(dotenv))
	for key, value := range dotenv {
		env[key] = value
	}

	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			env[key] = value
		}
	}
	return env, nil
}

func dotenvPath(workspace string) string {
	if strings.TrimSpace(workspace) != "" {
		return filepath.Join(workspace, ".env")
	}
	return ".env"
}

// StringOr returns env[key] when present and non-empty, otherwise fallback.
func StringOr(env map[string]string, key, fallback string) string {
	if value, ok := env[key]; ok && value != "" {
		return value
	}
	return fallback
}

// IntOr parses env[key] as an int, returning fallback when the key is unset.
func IntOr(env map[string]string, key string, fallback int) (int, error) {
	value, ok := env[key]
	if !ok || value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, value, err)
	}
	return number, nil
}

func readDotenv(path string) (map[string]string, error) {
	values, err := godotenv.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return values, nil
}
