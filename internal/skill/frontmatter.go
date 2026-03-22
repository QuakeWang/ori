package skill

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// parseFrontmatter splits a SKILL.md file into frontmatter (YAML) and body.
// Returns the parsed frontmatter map, the remaining body text, and any error.
// If no frontmatter delimiter is found, returns an empty map and the full content as body.
func parseFrontmatter(content string) (map[string]any, string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, strings.TrimSpace(content), nil
	}

	// Find the closing "---".
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}

	if endIdx < 0 {
		// No closing delimiter; treat entire content as body.
		return nil, strings.TrimSpace(content), nil
	}

	yamlBlock := strings.Join(lines[1:endIdx], "\n")
	body := strings.TrimSpace(strings.Join(lines[endIdx+1:], "\n"))

	var raw map[string]any
	if err := yaml.Unmarshal([]byte(yamlBlock), &raw); err != nil {
		return nil, body, nil // malformed YAML: skip frontmatter, keep body
	}

	// Normalize keys to lowercase.
	normalized := make(map[string]any, len(raw))
	for k, v := range raw {
		normalized[strings.ToLower(k)] = v
	}

	return normalized, body, nil
}
