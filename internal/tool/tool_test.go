package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------ name rewriting

func TestModelName(t *testing.T) {
	assert.Equal(t, "bash_output", ModelName("bash.output"))
	assert.Equal(t, "tape_info", ModelName("tape.info"))
	assert.Equal(t, "help", ModelName("help"))
}

func TestExternalName(t *testing.T) {
	assert.Equal(t, "bash.output", ExternalName("bash_output"))
	assert.Equal(t, "tape.info", ExternalName("tape_info"))
	assert.Equal(t, "help", ExternalName("help"))
}

// ------------------------------------------------------------------ registry

func echoHandler(ctx context.Context, tc *Context, input json.RawMessage) (*Result, error) {
	return &Result{Text: string(input)}, nil
}

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	require.NoError(t, r.Register(&Tool{
		Spec:    Spec{Name: "echo", Description: "Echo text"},
		Handler: echoHandler,
	}))
	require.NoError(t, r.Register(&Tool{
		Spec:    Spec{Name: "bash", Description: "Run a shell command", Dangerous: true},
		Handler: echoHandler,
	}))
	require.NoError(t, r.Register(&Tool{
		Spec:    Spec{Name: "bash.output", Description: "Read output", Dangerous: true},
		Handler: echoHandler,
	}))
	require.NoError(t, r.Register(&Tool{
		Spec:    Spec{Name: "fs.write", Description: "Write file", Dangerous: true},
		Handler: echoHandler,
	}))
	return r
}

func TestRegistry_Register_Duplicate(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&Tool{
		Spec: Spec{Name: "help"}, Handler: echoHandler,
	}))
	err := r.Register(&Tool{
		Spec: Spec{Name: "help"}, Handler: echoHandler,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestRegistry_Get(t *testing.T) {
	r := newTestRegistry(t)

	t1, ok := r.Get("bash")
	assert.True(t, ok)
	assert.Equal(t, "bash", t1.Spec.Name)

	_, ok = r.Get("nonexistent")
	assert.False(t, ok)
}

func TestRegistry_Execute_Success(t *testing.T) {
	r := newTestRegistry(t)
	tc := &Context{Workspace: "/tmp"}

	// Execute uses model name (underscored). No filter = allow all non-dangerous.
	result, err := r.Execute(context.Background(), tc, "echo", json.RawMessage(`{"text":"hi"}`), Filter{})
	require.NoError(t, err)
	assert.Contains(t, result.Text, "text")
}

func TestRegistry_Execute_UnknownTool(t *testing.T) {
	r := newTestRegistry(t)
	tc := &Context{}

	_, err := r.Execute(context.Background(), tc, "nonexistent", nil, Filter{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

func TestRegistry_Execute_DangerousBlocked(t *testing.T) {
	r := newTestRegistry(t)
	tc := &Context{}

	// Default filter: dangerous tools are blocked.
	result, err := r.Execute(context.Background(), tc, "fs_write", json.RawMessage(`{}`), Filter{})
	require.NoError(t, err)
	assert.Contains(t, result.Text, "not allowed")
}

func TestRegistry_Execute_DangerousAllowed(t *testing.T) {
	r := newTestRegistry(t)
	tc := &Context{}

	// Explicit filter allows dangerous tool.
	result, err := r.Execute(context.Background(), tc, "fs_write", json.RawMessage(`{"content":"ok"}`), Filter{
		AllowedTools: map[string]bool{"fs.write": true},
	})
	require.NoError(t, err)
	assert.NotContains(t, result.Text, "not allowed")
}

func TestRegistry_Schemas_NilFilter(t *testing.T) {
	r := newTestRegistry(t)

	// nil AllowedTools = all non-dangerous tools.
	schemas := r.Schemas(Filter{})
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		names = append(names, s.Name)
	}
	assert.Contains(t, names, "echo")
	assert.NotContains(t, names, "bash")
	assert.NotContains(t, names, "bash_output")
	assert.NotContains(t, names, "fs_write")
}

func TestRegistry_Schemas_ExplicitFilter(t *testing.T) {
	r := newTestRegistry(t)

	// Explicit filter can include dangerous tools.
	schemas := r.Schemas(Filter{AllowedTools: map[string]bool{
		"fs.write": true,
		"bash":     true,
	}})
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		names = append(names, s.Name)
	}
	assert.Len(t, names, 2)
	assert.Contains(t, names, "bash")
	assert.Contains(t, names, "fs_write")
}

func TestRegistry_RenderPrompt(t *testing.T) {
	r := newTestRegistry(t)
	prompt := r.RenderPrompt(Filter{})

	assert.Contains(t, prompt, "<available_tools>")
	assert.Contains(t, prompt, "</available_tools>")
	assert.Contains(t, prompt, "echo:")
	assert.NotContains(t, prompt, "bash:")
	assert.NotContains(t, prompt, "bash_output:")
	assert.NotContains(t, prompt, "fs_write")
}

func TestRegistry_RenderPrompt_Empty(t *testing.T) {
	r := NewRegistry()
	prompt := r.RenderPrompt(Filter{})
	assert.Equal(t, "", prompt)
}

func TestRegistry_List_Sorted(t *testing.T) {
	r := newTestRegistry(t)
	tools := r.List(Filter{})

	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Spec.Name)
	}
	// Should be sorted alphabetically.
	assert.Equal(t, []string{"echo"}, names)
}

func TestRegistry_List_DenyAll(t *testing.T) {
	r := newTestRegistry(t)
	// Non-nil empty AllowedTools = deny all.
	tools := r.List(Filter{AllowedTools: map[string]bool{}})
	assert.Empty(t, tools, "deny-all filter should return no tools")
}

func TestRegistry_Execute_DenyAll(t *testing.T) {
	r := newTestRegistry(t)
	tc := &Context{}
	// Non-nil empty AllowedTools = deny all; even non-dangerous tools are blocked.
	result, err := r.Execute(context.Background(), tc, "bash", json.RawMessage(`{}`), Filter{
		AllowedTools: map[string]bool{},
	})
	require.NoError(t, err)
	assert.Contains(t, result.Text, "not allowed", "deny-all should block even bash")
}

// ------------------------------------------------------------------ command parsing

func TestParseCommandDetailed_Simple(t *testing.T) {
	name, args, err := ParseCommandDetailed(",help")
	require.NoError(t, err)
	assert.Equal(t, "help", name)
	assert.Empty(t, args.Positional)
	assert.Empty(t, args.Keywords)
}

func TestParseCommandDetailed_WithKeywordArgs(t *testing.T) {
	name, args, err := ParseCommandDetailed(",skill name=foo")
	require.NoError(t, err)
	assert.Equal(t, "skill", name)
	assert.Equal(t, "foo", args.Keywords["name"].Text)
}

func TestParseCommandDetailed_QuotedValue(t *testing.T) {
	name, args, err := ParseCommandDetailed(",bash cmd='sleep 5' background=true")
	require.NoError(t, err)
	assert.Equal(t, "bash", name)
	assert.Equal(t, "sleep 5", args.Keywords["cmd"].Text)
	assert.True(t, args.Keywords["cmd"].Quoted)
	assert.Equal(t, "true", args.Keywords["background"].Text)
	assert.False(t, args.Keywords["background"].Quoted)
}

func TestParseCommandDetailed_PreservesQuotingForTypeInference(t *testing.T) {
	name, args, err := ParseCommandDetailed(`,typed background=true limit=3 name='007' note="hello world"`)
	require.NoError(t, err)
	assert.Equal(t, "typed", name)

	jsonArgs := CommandJSONArgs(args)
	assert.Equal(t, true, jsonArgs["background"])
	assert.Equal(t, 3, jsonArgs["limit"])
	assert.Equal(t, "007", jsonArgs["name"])
	assert.Equal(t, "hello world", jsonArgs["note"])
}

func TestParseCommandDetailed_DottedName(t *testing.T) {
	name, _, err := ParseCommandDetailed(",tape.info")
	require.NoError(t, err)
	assert.Equal(t, "tape.info", name)
}

func TestParseCommandDetailed_PositionalAfterKeyword(t *testing.T) {
	_, _, err := ParseCommandDetailed(",foo key=val positional")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "positional argument")
}

func TestParseCommandDetailed_UnterminatedQuote(t *testing.T) {
	_, _, err := ParseCommandDetailed(",bash cmd='unclosed")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unterminated")
}

func TestParseCommandDetailed_NotACommand(t *testing.T) {
	_, _, err := ParseCommandDetailed("help")
	assert.Error(t, err)
}

func TestParseCommandDetailed_Empty(t *testing.T) {
	_, _, err := ParseCommandDetailed(",")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty command")
}
