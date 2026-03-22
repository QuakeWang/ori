package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/tool"
)

// SubagentRunner is the function signature for running a sub-agent turn.
// Defined in package builtin (not agent) to break the import cycle.
// The real implementation is injected via RegisterSubagent at boot time.
type SubagentRunner func(
	ctx context.Context,
	sessionID string,
	prompt string,
	model string,
	allowedTools []string,
	allowedSkills []string,
	parentState *session.State,
) (output string, err error)

type subagentInput struct {
	Prompt        string    `json:"prompt"`
	Model         string    `json:"model,omitempty"`
	Session       string    `json:"session,omitempty"` // "temp" (default) | "inherit" | custom ID
	AllowedTools  []string  `json:"allowed_tools,omitempty"`
	AllowedSkills *[]string `json:"allowed_skills,omitempty"` // nil = inherit parent, [] = deny all
}

// RegisterSubagent replaces the stub subagent tool (registered by RegisterCore)
// with the real implementation backed by the given runner.
func RegisterSubagent(r *tool.Registry, runner SubagentRunner) {
	r.Replace(subagentTool(runner))
}

// subagentStub returns a tool with the subagent spec but a no-op handler.
// Registered by RegisterCore so `ori tools` always lists subagent.
// Replaced with the real handler by bootstrap.go after agent construction.
func subagentStub() *tool.Tool {
	return &tool.Tool{
		Spec: subagentSpec(),
		Handler: func(_ context.Context, _ *tool.Context, _ json.RawMessage) (*tool.Result, error) {
			return &tool.Result{Text: "(subagent is not available in this context)"}, nil
		},
	}
}

func subagentSpec() tool.Spec {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {
				"type": "string",
				"description": "The task prompt for the sub-agent."
			},
			"model": {
				"type": "string",
				"description": "Optional model override for the sub-agent."
			},
			"session": {
				"type": "string",
				"description": "Session strategy: 'temp' (default, isolated), 'inherit' (share parent session), or a custom session ID."
			},
			"allowed_tools": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Tool whitelist. Omit to inherit parent boundary. Set to [] to deny all tools."
			},
			"allowed_skills": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Skill whitelist. Omit to inherit parent boundary. Set to [] to deny all skills."
			}
		},
		"required": ["prompt"]
	}`)

	return tool.Spec{
		Name: "subagent",
		Description: "Run a task with a sub-agent using an isolated session. " +
			"Use this to delegate sub-tasks that need their own context.",
		Schema: schema,
	}
}

func subagentTool(runner SubagentRunner) *tool.Tool {
	return &tool.Tool{
		Spec: subagentSpec(),
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in subagentInput
			if err := decodeToolInput(input, &in, "subagent"); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Prompt) == "" {
				return nil, fmt.Errorf("prompt is required")
			}

			sessionID := resolveSubagentSession(in.Session, tc.SessionID)
			allowedTools := resolveInheritedTools(in.AllowedTools, tc.State)
			allowedSkills := resolveInheritedSkills(in.AllowedSkills, tc.State)

			output, err := runner(ctx, sessionID, in.Prompt, in.Model, allowedTools, allowedSkills, tc.State)
			if err != nil {
				return nil, fmt.Errorf("subagent failed: %w", err)
			}
			return &tool.Result{Text: output}, nil
		},
	}
}

// resolveInheritedTools determines the tool allowlist for a sub-agent.
//   - nil (omitted): inherit parent boundary
//   - [] (explicit empty): deny all
//   - non-empty: use as-is
func resolveInheritedTools(explicit []string, parent *session.State) []string {
	if explicit != nil {
		return explicit
	}
	if parent == nil || parent.AllowedTools == nil {
		return nil // no boundary
	}
	return session.AllowlistToSlice(parent.AllowedTools)
}

// resolveInheritedSkills determines the skill allowlist for a sub-agent.
//   - nil AllowedSkills (omitted): inherit parent boundary or activated skills
//   - non-nil (incl. empty): use as-is (empty = deny all)
func resolveInheritedSkills(explicit *[]string, parent *session.State) []string {
	if explicit != nil {
		return *explicit
	}
	if parent == nil {
		return nil
	}
	if parent.AllowedSkills != nil {
		return session.AllowlistToSlice(parent.AllowedSkills)
	}
	// No boundary → fall back to activated skills.
	// If parent has no activated skills either, return nil (= unrestricted)
	// instead of empty slice (= deny-all).
	if len(parent.ActivatedSkills) == 0 {
		return nil
	}
	return activatedSkillNames(parent)
}

func activatedSkillNames(state *session.State) []string {
	result := make([]string, 0, len(state.ActivatedSkills))
	for name := range state.ActivatedSkills {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func resolveSubagentSession(strategy, parentSession string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "inherit":
		return parentSession
	case "", "temp":
		return "temp/" + shortID()
	default:
		return strategy
	}
}

func shortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
