package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuakeWang/ori/internal/llm"
)

// Registry is a thread-safe, central registry of tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool // keyed by external dotted name
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]*Tool),
	}
}

// Register adds a tool to the registry. Returns an error if a tool with
// the same name is already registered.
func (r *Registry) Register(t *Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[t.Spec.Name]; exists {
		return fmt.Errorf("tool %q is already registered", t.Spec.Name)
	}
	r.tools[t.Spec.Name] = t
	slog.Debug("tool.registered", "name", t.Spec.Name)
	return nil
}

// Replace overwrites an existing tool entry. This is used when a stub
// registration (e.g. from RegisterCore) needs to be upgraded to a real
// handler at boot time. If the tool does not exist, it registers it.
func (r *Registry) Replace(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Spec.Name] = t
	slog.Debug("tool.replaced", "name", t.Spec.Name)
}

// Get looks up a tool by its external dotted name.
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Execute dispatches a tool call with runtime policy enforcement.
// The name parameter is the external dotted name.
// The filter controls which tools are executable (same rules as schema visibility).
// Dangerous tools are rejected unless the filter explicitly allows them.
func (r *Registry) Execute(ctx context.Context, tc *Context, name string, input json.RawMessage, f Filter) (*Result, error) {
	extName := ExternalName(name)

	r.mu.RLock()
	t, ok := r.tools[extName]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown tool %q", extName)
	}

	// Runtime policy: reject tools that don't pass the filter.
	if !r.allowed(t, f) {
		slog.Warn("tool.call.blocked", "name", extName, "reason", "not allowed by policy")
		return &Result{Text: fmt.Sprintf("(tool '%s' is not allowed in this context)", extName)}, nil
	}

	start := time.Now()
	slog.Info("tool.call.start", "name", extName)

	result, err := safeExecute(ctx, t, tc, input)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		slog.Error("tool.call.error", "name", extName, "elapsed_ms", elapsed, "error", err)
		return nil, fmt.Errorf("tool %q failed: %w", extName, err)
	}

	slog.Info("tool.call.done", "name", extName, "elapsed_ms", elapsed, "truncated", result.Truncated)
	return result, nil
}

// safeExecute wraps a tool handler call with panic recovery.
func safeExecute(ctx context.Context, t *Tool, tc *Context, input json.RawMessage) (result *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool handler panic: %v", r)
		}
	}()
	return t.Handler(ctx, tc, input)
}

// Schemas returns model-facing tool schemas for tools passing the filter.
// Dangerous tools are excluded unless explicitly allowed.
func (r *Registry) Schemas(f Filter) []llm.ToolSchema {
	tools := r.List(f)
	schemas := make([]llm.ToolSchema, 0, len(tools))
	for _, t := range tools {
		schemas = append(schemas, llm.ToolSchema{
			Name:        ModelName(t.Spec.Name),
			Description: t.Spec.Description,
			Schema:      t.Spec.Schema,
		})
	}
	return schemas
}

// RenderPrompt generates the <available_tools> prompt block.
func (r *Registry) RenderPrompt(f Filter) string {
	tools := r.List(f)
	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<available_tools>\n")
	for _, t := range tools {
		b.WriteString("- ")
		b.WriteString(ModelName(t.Spec.Name))
		if t.Spec.Description != "" {
			b.WriteString(": ")
			b.WriteString(t.Spec.Description)
		}
		b.WriteByte('\n')
	}
	b.WriteString("</available_tools>")
	return b.String()
}

// List returns tools that pass the filter, sorted by name.
// Dangerous tools are excluded unless explicitly present in the filter.
func (r *Registry) List(f Filter) []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if !r.allowed(t, f) {
			continue
		}
		result = append(result, t)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Spec.Name < result[j].Spec.Name
	})
	return result
}

func (r *Registry) allowed(t *Tool, f Filter) bool {
	// IncludeAll: show everything (for ori tools listing).
	if f.IncludeAll {
		return true
	}
	// If a whitelist is set (non-nil), only allowed tools pass.
	// Non-nil empty map = deny all tools.
	if f.AllowedTools != nil {
		return f.AllowedTools[t.Spec.Name]
	}
	// Without a filter (nil), dangerous tools are excluded by default.
	return !t.Spec.Dangerous
}

// ModelName converts an external dotted name to a model-facing underscored name.
// e.g. "bash.output" -> "bash_output"
func ModelName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// ExternalName converts a model-facing underscored name back to a dotted name.
// e.g. "bash_output" -> "bash.output"
func ExternalName(name string) string {
	return strings.ReplaceAll(name, "_", ".")
}
