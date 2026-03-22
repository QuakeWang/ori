package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/tool"
)

type skillInput struct {
	Name string `json:"name"`
}

func skillTool(svc *skill.Service) *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "skill",
			Description: "Load the skill content by name. Return the location and skill content.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"The skill name to load"}},"required":["name"]}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in skillInput
			if err := decodeToolInput(input, &in, "skill"); err != nil {
				return nil, err
			}

			name, err := normalizedSkillName(in.Name)
			if err != nil {
				return nil, err
			}

			// Check allowed skills if a whitelist is set.
			// nil means "no restriction"; non-nil (even empty) means "enforce".
			if tc.State != nil && !skill.Allowed(tc.State.AllowedSkills, name) {
				return &tool.Result{
					Text: fmt.Sprintf("(skill '%s' is not allowed in this context)", name),
				}, nil
			}

			// Use pre-discovered catalog from context if available;
			// otherwise fall back to a fresh discovery.
			skills, err := discoverToolSkills(tc, svc)
			if err != nil {
				return nil, err
			}

			found := findSkillByName(skills, name)
			if found == nil {
				return &tool.Result{Text: "(no such skill)"}, nil
			}

			// Record activation in session state, including max_steps from frontmatter.
			activateSkill(tc.State, found)
			return loadedSkillResult(found), nil
		},
	}
}

func normalizedSkillName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("skill name is required")
	}
	return name, nil
}

func discoverToolSkills(tc *tool.Context, svc *skill.Service) ([]skill.Skill, error) {
	if tc.Skills != nil {
		return tc.Skills, nil
	}

	discovered, err := svc.Discover(tc.Workspace)
	if err != nil {
		return nil, fmt.Errorf("skill discovery failed: %w", err)
	}
	return discovered, nil
}

func findSkillByName(skills []skill.Skill, name string) *skill.Skill {
	for i := range skills {
		if strings.EqualFold(skills[i].Name, name) {
			return &skills[i]
		}
	}
	return nil
}

func activateSkill(state *session.State, found *skill.Skill) {
	if state == nil {
		return
	}
	act := skill.BuildActivation(found.Name, found.Frontmatter)
	if state.ActivatedSkills == nil {
		state.ActivatedSkills = make(map[string]session.Activation)
	}
	state.ActivatedSkills[found.Name] = act
	skill.ApplyActivationRuntimeState(state, act)
}

func loadedSkillResult(found *skill.Skill) *tool.Result {
	body := found.Body
	if body == "" {
		body = "(no content)"
	}
	return &tool.Result{
		Text: fmt.Sprintf("Location: %s\n---\n%s", found.Location, body),
	}
}
