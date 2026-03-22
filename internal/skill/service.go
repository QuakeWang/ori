package skill

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/QuakeWang/ori/internal/session"
)

const (
	projectSkillsDir = ".agents/skills"
	legacySkillsDir  = ".agent/skills"
	skillFileName    = "SKILL.md"
	maxNameLen       = 64
	maxDescLen       = 1024
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var hintPattern = regexp.MustCompile(`(?:^|[^a-z0-9-])\$([a-z0-9]+(?:-[a-z0-9]+)*)`)

// Service provides skill discovery and prompt rendering.
type Service struct {
	builtinSources []BuiltinSource
}

// BuiltinSource describes one embedded skill root assembled into the runtime.
type BuiltinSource struct {
	Name  string
	FS    fs.FS
	Allow map[string]bool
}

// NewServiceWithSources creates a service from explicitly ordered builtin
// skill sources. Earlier sources win on duplicate builtin skill names.
func NewServiceWithSources(sources ...BuiltinSource) *Service {
	return &Service{builtinSources: normalizeBuiltinSources(sources)}
}

// Discover finds skills from project, legacy-project, global, and builtin roots
// with override precedence: project > legacy-project > global > builtin.
// The first occurrence of a name wins.
func (s *Service) Discover(workspace string) ([]Skill, error) {
	seen := make(map[string]bool)
	var skills []Skill

	// Filesystem-backed roots (project / legacy / global).
	for _, root := range s.fsRoots(workspace) {
		info, err := os.Stat(root.path)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("skill.discovery.stat.error", "path", root.path, "error", err)
			}
			continue
		}
		if !info.IsDir() {
			continue
		}

		entries, err := os.ReadDir(root.path)
		if err != nil {
			slog.Warn("skill.discovery.readdir.error", "path", root.path, "error", err)
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			sk, err := readSkillFromFS(os.DirFS(root.path), entry.Name(), root.source)
			if err != nil {
				slog.Debug("skill.discovery.skip", "dir", entry.Name(), "reason", err)
				continue
			}

			key := strings.ToLower(sk.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			// Compute absolute location for filesystem skills.
			absLoc, err := filepath.Abs(filepath.Join(root.path, entry.Name(), skillFileName))
			if err != nil {
				absLoc = filepath.Join(root.path, entry.Name(), skillFileName)
			}
			sk.Location = absLoc
			// Perform ${SKILL_DIR} substitution using the real directory.
			sk.Body = strings.ReplaceAll(sk.Body, "${SKILL_DIR}", filepath.Dir(absLoc))
			skills = append(skills, *sk)
		}
	}

	for _, source := range s.builtinSources {
		skills = append(skills, s.discoverBuiltinSource(source, seen)...)
	}

	// Sort by name for deterministic output.
	sort.Slice(skills, func(i, j int) bool {
		return strings.ToLower(skills[i].Name) < strings.ToLower(skills[j].Name)
	})
	return skills, nil
}

// RenderPrompt generates the skill prompt block.
// All discovered skills appear in <available_skills>.
// Only activated skills get <active_skill_instructions> blocks.
func (s *Service) RenderPrompt(skills []Skill, activated map[string]session.Activation) string {
	if len(skills) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<available_skills>\n")
	for _, sk := range skills {
		b.WriteString("- ")
		b.WriteString(sk.Name)
		b.WriteString(": ")
		b.WriteString(sk.Description)
		b.WriteByte('\n')
	}
	b.WriteString("</available_skills>")

	for _, sk := range skills {
		if _, ok := activated[sk.Name]; !ok {
			continue
		}
		if sk.Body == "" {
			continue
		}
		b.WriteString("\n\n")
		_, _ = fmt.Fprintf(&b, "<active_skill_instructions name=%q>\n", sk.Name)
		b.WriteString("Follow these instructions as your workflow guide. Adapt your output style and depth to the specific situation.\n")
		b.WriteString(sk.Body)
		b.WriteString("\n</active_skill_instructions>")
	}

	return b.String()
}

// ResolveHints scans the input for skill names prefixed with "$"
// and returns matching skill name hints.
// This matches the design contract: $skill-name activates a skill.
func (s *Service) ResolveHints(input string) []string {
	var hints []string
	for _, match := range hintPattern.FindAllStringSubmatch(strings.ToLower(input), -1) {
		if len(match) < 2 {
			continue
		}
		candidate := strings.TrimSpace(match[1])
		if candidate != "" && namePattern.MatchString(candidate) {
			hints = append(hints, candidate)
		}
	}
	return hints
}

type skillRoot struct {
	path   string
	source string
}

func (s *Service) fsRoots(workspace string) []skillRoot {
	var roots []skillRoot

	// Project skills.
	if workspace != "" {
		roots = append(roots, skillRoot{
			path:   filepath.Join(workspace, projectSkillsDir),
			source: "project",
		})

		// Legacy project skills.
		legacyPath := filepath.Join(workspace, legacySkillsDir)
		if info, err := os.Stat(legacyPath); err == nil && info.IsDir() {
			slog.Warn("skill.legacy_dir_found",
				"path", legacyPath,
				"msg", fmt.Sprintf("Found legacy skills directory at '%s'. Please move to '%s'.", legacyPath, projectSkillsDir))
			roots = append(roots, skillRoot{path: legacyPath, source: "project"})
		}
	}

	// Global skills.
	home, err := os.UserHomeDir()
	if err == nil {
		roots = append(roots, skillRoot{
			path:   filepath.Join(home, projectSkillsDir),
			source: "global",
		})
	}

	return roots
}

func (s *Service) discoverBuiltinSource(source BuiltinSource, seen map[string]bool) []Skill {
	if source.FS == nil {
		return nil
	}

	entries, err := fs.ReadDir(source.FS, ".")
	if err != nil {
		slog.Warn("skill.discovery.readdir.builtin.error", "source", source.logName(), "error", err)
		return nil
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if len(source.Allow) > 0 && !source.Allow[strings.ToLower(entry.Name())] {
			continue
		}

		sk, err := readSkillFromFS(source.FS, entry.Name(), "builtin")
		if err != nil {
			slog.Debug("skill.discovery.skip.builtin", "source", source.logName(), "dir", entry.Name(), "reason", err)
			continue
		}

		key := strings.ToLower(sk.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		sk.Location = builtinSkillLocation(source.Name, entry.Name())
		skills = append(skills, *sk)
	}
	return skills
}

func normalizeBuiltinSources(sources []BuiltinSource) []BuiltinSource {
	normalized := make([]BuiltinSource, 0, len(sources))
	for _, source := range sources {
		if source.FS == nil {
			continue
		}
		normalized = append(normalized, BuiltinSource{
			Name:  strings.ToLower(strings.TrimSpace(source.Name)),
			FS:    source.FS,
			Allow: normalizeAllowlist(source.Allow),
		})
	}
	return normalized
}

func normalizeAllowlist(allow map[string]bool) map[string]bool {
	if len(allow) == 0 {
		return nil
	}

	normalized := make(map[string]bool, len(allow))
	for name, enabled := range allow {
		if enabled {
			normalized[strings.ToLower(name)] = true
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func builtinSkillLocation(sourceName, dir string) string {
	sourceName = strings.TrimSpace(sourceName)
	if sourceName == "" || sourceName == "builtin" {
		return fmt.Sprintf("builtin://%s/%s", dir, skillFileName)
	}
	return fmt.Sprintf("builtin://%s/%s/%s", sourceName, dir, skillFileName)
}

func (s BuiltinSource) logName() string {
	if strings.TrimSpace(s.Name) == "" {
		return "builtin"
	}
	return s.Name
}

// readSkillFromFS reads and validates a SKILL.md from any fs.FS.
// The dir parameter is the directory name within the FS.
// Location and ${SKILL_DIR} substitution are handled by the caller.
func readSkillFromFS(fsys fs.FS, dir, source string) (*Skill, error) {
	skillPath := filepath.Join(dir, skillFileName)
	data, err := fs.ReadFile(fsys, skillPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", skillPath, err)
	}

	fm, body, err := parseFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("cannot parse frontmatter: %w", err)
	}

	// Validate name.
	name, _ := fm["name"].(string)
	name = strings.TrimSpace(name)
	if err := validateName(name, dir); err != nil {
		return nil, err
	}

	// Validate description.
	desc, _ := fm["description"].(string)
	desc = strings.TrimSpace(desc)
	if err := validateDescription(desc); err != nil {
		return nil, err
	}

	return &Skill{
		Name:        name,
		Description: desc,
		Source:      source,
		Body:        body,
		Frontmatter: fm,
	}, nil
}

func validateName(name, dirName string) error {
	if name == "" {
		return fmt.Errorf("skill name is empty")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("skill name %q exceeds max length %d", name, maxNameLen)
	}
	if name != dirName {
		return fmt.Errorf("skill name %q does not match directory %q", name, dirName)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("skill name %q does not match pattern %s", name, namePattern.String())
	}
	return nil
}

func validateDescription(desc string) error {
	if desc == "" {
		return fmt.Errorf("skill description is empty")
	}
	if len(desc) > maxDescLen {
		return fmt.Errorf("skill description exceeds max length %d", maxDescLen)
	}
	return nil
}
