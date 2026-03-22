package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
)

// DefaultSystemPrompt is the base system prompt for the agent.
// Adapted from dod's DEFAULT_SYSTEM_PROMPT with sections relevant to
// local CLI usage. The <response_instruct> section from dod is omitted
// because ori renders output directly in the local CLI.
const DefaultSystemPrompt = `<general_instruct>
Call tools or skills to finish the task.
</general_instruct>
<output_style>
You MUST follow these output rules:
- Answer in Chinese when user writes in Chinese.
- Use natural language for analysis, conclusions, and causal reasoning.
- Use tables or lists only for structured evidence data (query results, metric lists, findings).
- When synthesizing data into tables, preserve the exact values of identifiers (like Query IDs) and code snippets (like SQL). Do not truncate, translate, or abbreviate them.
- Do not use Markdown headings (#, ##, ###). Use **bold text** as section labels instead.
- State each point once. Do not repeat why something is a problem.
- Avoid contrastive fluff or conversational filler like "The root cause is not X, but Y". State findings and root causes directly.
- Do not rephrase the same finding across multiple sections or tables. Consolidate into one place.
- When giving recommendations, keep them compact. Do not write multi-section essays.
- Do not proactively explain principles or add educational content unless the user asks.
- Inspection results are pure data findings. Do not mix in optimization advice unless the user asks.
- When asked "who are you" or to list your capabilities, provide a structured, professional summary. Use Markdown tables to categorize your Doris diagnostic skills (e.g., Cluster Inspection, SQL Diagnostics, Schema Audit) and provide bullet points with concrete example questions the user can ask.
</output_style>
<context_contract>
Excessively long context may cause model call failures. In this case, you MAY use tape.info to check token usage and you SHOULD use tape.handoff tool to shorten the length of the retrieved history.
</context_contract>`

// buildSystemPrompt assembles the full system prompt using the provided skill
// catalog. $skill hints are prompt-local: they expand active instructions for
// the current turn but do not persist into session state.
func (a *Agent) buildSystemPrompt(state *session.State, opts RunOptions, skills []skill.Skill, input string) string {
	var parts []string

	parts = append(parts, DefaultSystemPrompt)

	if agents := readAgentsFile(state.Workspace); agents != "" {
		parts = append(parts, fmt.Sprintf("<user_rules>\n%s\n</user_rules>", agents))
	}

	toolFilter := a.toolFilter(opts, state)
	if tp := a.tools.RenderPrompt(toolFilter); tp != "" {
		parts = append(parts, tp)
	}

	// Filter skills by AllowedSkills whitelist if set.
	if len(skills) > 0 {
		skills = skill.FilterByAllowlist(skills, state.AllowedSkills)
		if len(skills) > 0 {
			if sp := a.skills.RenderPrompt(skills, a.promptSkillActivations(state, skills, input)); sp != "" {
				parts = append(parts, sp)
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

func (a *Agent) promptSkillActivations(
	state *session.State,
	skills []skill.Skill,
	input string,
) map[string]session.Activation {
	if state == nil {
		return nil
	}

	active := make(map[string]session.Activation, len(state.ActivatedSkills))
	for name, act := range state.ActivatedSkills {
		active[name] = act
	}

	if a.skills == nil || strings.TrimSpace(input) == "" {
		if len(active) == 0 {
			return nil
		}
		return active
	}

	skillIndex := indexSkillsByName(skills)
	for _, name := range a.skills.ResolveHints(input) {
		if !skill.Allowed(state.AllowedSkills, name) {
			continue
		}

		item, ok := skillIndex[strings.ToLower(name)]
		if !ok {
			continue
		}
		if _, exists := active[item.Name]; exists {
			continue
		}
		active[item.Name] = session.Activation{Name: item.Name}
	}

	if len(active) == 0 {
		return nil
	}
	return active
}

// readAgentsFile reads the workspace AGENTS.md file.
func readAgentsFile(workspace string) string {
	path := filepath.Join(workspace, "AGENTS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
