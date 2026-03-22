package ui

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// ConfigureLogging installs the default slog handler for CLI commands.
func ConfigureLogging(verbose int) {
	level := slog.LevelWarn
	switch {
	case verbose >= 2:
		level = slog.LevelDebug
	case verbose == 1:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// ConfigureLoggingFromEnv initializes logging before config loading happens.
func ConfigureLoggingFromEnv() {
	raw := strings.TrimSpace(os.Getenv("ORI_VERBOSE"))
	if raw == "" {
		ConfigureLogging(0)
		return
	}

	verbose, err := strconv.Atoi(raw)
	if err != nil {
		ConfigureLogging(0)
		return
	}
	ConfigureLogging(verbose)
}
