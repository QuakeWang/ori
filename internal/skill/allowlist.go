package skill

import "strings"

func Allowed(allowed map[string]bool, name string) bool {
	if allowed == nil {
		return true
	}
	return allowed[strings.ToLower(name)]
}

func FilterByAllowlist(skills []Skill, allowed map[string]bool) []Skill {
	if allowed == nil {
		return skills
	}

	filtered := make([]Skill, 0, len(skills))
	for _, sk := range skills {
		if Allowed(allowed, sk.Name) {
			filtered = append(filtered, sk)
		}
	}
	return filtered
}
