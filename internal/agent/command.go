package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

// maxEventOutputLen caps the output stored in command event entries.
const maxEventOutputLen = 500

// runCommand handles command mode input (lines starting with ",").
// Commands use external dotted tool names.
// Unknown commands return an error with suggestions.
//
// This delegates to tool.ParseCommand for parsing so that argument
// validation rules (e.g. no positional after keyword) are unified.
func (a *Agent) runCommand(
	ctx context.Context,
	sess *session.Session,
	st store.Store,
	line string,
	opts RunOptions,
) (*RunResult, error) {
	start := time.Now()

	// ParseCommandDetailed expects the leading ",".
	name, args, err := tool.ParseCommandDetailed("," + line)
	if err != nil {
		return &RunResult{SessionID: sess.ID, Output: fmt.Sprintf("Error: %v", err)}, nil
	}

	// Convert parsed args to JSON for tool execution.
	input, _ := json.Marshal(tool.CommandJSONArgs(args))

	// Build the same filter used by the loop for policy consistency.
	// Command mode escalates to IncludeAll only when no allowlist is set (nil).
	// A non-nil empty AllowedTools means "deny all" and must not be escalated.
	filter := a.toolFilter(opts, sess.State)
	if filter.AllowedTools == nil {
		filter.IncludeAll = true
	}

	// Try to execute as a registered tool.
	if _, ok := a.tools.Get(name); ok {
		discoveredSkills, discoverErr := a.discoverSkills(sess.State.Workspace)
		if discoverErr != nil {
			slog.Warn("agent.skill_discovery_failed",
				"workspace", sess.State.Workspace,
				"phase", "command",
				"error", discoverErr)
		}
		toolCtx := a.newToolContext(sess, st, discoveredSkills)
		result, err := a.tools.Execute(ctx, toolCtx, name, json.RawMessage(input), filter)
		if err != nil {
			a.recordCommandEvent(st, sess.ID, line, name, "error", time.Since(start), err.Error())
			return &RunResult{
				SessionID: sess.ID,
				Output:    fmt.Sprintf("Error: %v", err),
			}, nil
		}
		// Skip event recording after tape.reset to avoid polluting the cleared tape.
		if !metaFlag(result.Meta, "reset") {
			a.recordCommandEvent(st, sess.ID, line, name, "ok", time.Since(start), result.Text)
		}
		return &RunResult{
			SessionID: sess.ID,
			Output:    result.Text,
			Meta:      result.Meta,
		}, nil
	}

	// Unknown command: return error with suggestions.
	suggestion := a.buildUnknownCommandMsg(name)
	return &RunResult{SessionID: sess.ID, Output: suggestion}, nil
}

// recordCommandEvent persists a command execution event to the store.
func (a *Agent) recordCommandEvent(st store.Store, sessionID, raw, name, status string, elapsed time.Duration, output string) {
	// Truncate output to avoid bloating the store.
	if len(output) > maxEventOutputLen {
		output = output[:maxEventOutputLen] + "..."
	}
	entry := store.NewEventEntry(map[string]any{
		"type":       "command",
		"raw":        raw,
		"name":       name,
		"status":     status,
		"elapsed_ms": elapsed.Milliseconds(),
		"output":     output,
	})
	_ = st.Append(sessionID, entry)
}

// buildUnknownCommandMsg returns a helpful error for unrecognized commands.
func (a *Agent) buildUnknownCommandMsg(name string) string {
	matches := a.matchingToolNames(name)
	lines := []string{fmt.Sprintf("Unknown command ',%s'.", name)}
	if len(matches) > 0 {
		lines = append(lines, "Did you mean:")
		for _, m := range matches {
			lines = append(lines, "  ,"+m)
		}
	}
	lines = append(lines, "Use ',help' to list available commands.")
	lines = append(lines, "Use ',bash cmd=...' to run shell commands.")
	return strings.Join(lines, "\n")
}

func (a *Agent) matchingToolNames(name string) []string {
	tools := a.tools.List(tool.Filter{IncludeAll: true})
	matches := make([]string, 0, len(tools))
	for _, candidate := range tools {
		if strings.HasPrefix(candidate.Spec.Name, name) {
			matches = append(matches, candidate.Spec.Name)
		}
	}
	if len(matches) > 0 {
		return limitMatches(matches, 8)
	}

	section, _, hasSection := strings.Cut(name, ".")
	if !hasSection || section == "" {
		return nil
	}
	for _, candidate := range tools {
		if strings.HasPrefix(candidate.Spec.Name, section+".") {
			matches = append(matches, candidate.Spec.Name)
		}
	}
	return limitMatches(matches, 8)
}

func limitMatches(matches []string, limit int) []string {
	if len(matches) <= limit {
		return matches
	}
	return matches[:limit]
}

// metaFlag returns true if meta[key] is a truthy bool.
func metaFlag(meta map[string]any, key string) bool {
	if meta == nil {
		return false
	}
	v, _ := meta[key].(bool)
	return v
}
