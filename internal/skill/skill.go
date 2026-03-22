package skill

// Skill represents a discovered skill backed by a SKILL.md file.
type Skill struct {
	Name        string         // kebab-case, must match directory name
	Description string         // human-readable description (from frontmatter)
	Source      string         // "project" | "global" | "builtin"
	Location    string         // absolute path to SKILL.md
	Body        string         // body content (frontmatter stripped, ${SKILL_DIR} resolved)
	Frontmatter map[string]any // all frontmatter fields preserved as opaque metadata
}
