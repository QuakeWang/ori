package skill

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/session"
)

// ------------------------------------------------------------------ frontmatter parsing

func TestParseFrontmatter_Normal(t *testing.T) {
	content := "---\nname: test-skill\ndescription: A test skill\nmax_steps: 5\n---\nThis is the body."
	fm, body, err := parseFrontmatter(content)
	require.NoError(t, err)
	assert.Equal(t, "test-skill", fm["name"])
	assert.Equal(t, "A test skill", fm["description"])
	assert.Equal(t, 5, fm["max_steps"])
	assert.Equal(t, "This is the body.", body)
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	content := "Just a body with no frontmatter."
	fm, body, err := parseFrontmatter(content)
	require.NoError(t, err)
	assert.Nil(t, fm)
	assert.Equal(t, "Just a body with no frontmatter.", body)
}

func TestParseFrontmatter_EmptyBody(t *testing.T) {
	content := "---\nname: test\ndescription: test desc\n---"
	fm, body, err := parseFrontmatter(content)
	require.NoError(t, err)
	assert.Equal(t, "test", fm["name"])
	assert.Equal(t, "", body)
}

func TestParseFrontmatter_MalformedYAML(t *testing.T) {
	content := "---\n: invalid yaml {{{\n---\nBody here."
	fm, body, err := parseFrontmatter(content)
	require.NoError(t, err)
	assert.Nil(t, fm)
	assert.Equal(t, "Body here.", body)
}

func TestParseFrontmatter_NoClosingDelimiter(t *testing.T) {
	content := "---\nname: test\nno closing delimiter"
	fm, body, err := parseFrontmatter(content)
	require.NoError(t, err)
	assert.Nil(t, fm)
	assert.Equal(t, content, body)
}

func TestParseFrontmatter_KeysLowercase(t *testing.T) {
	content := "---\nName: test\nDescription: desc\nBlocked_SQL: foo\n---\nbody"
	fm, _, err := parseFrontmatter(content)
	require.NoError(t, err)
	assert.Equal(t, "test", fm["name"])
	assert.Equal(t, "desc", fm["description"])
	assert.Equal(t, "foo", fm["blocked_sql"])
}

// ------------------------------------------------------------------ name validation

func TestValidateName_Valid(t *testing.T) {
	assert.NoError(t, validateName("test-skill", "test-skill"))
	assert.NoError(t, validateName("abc", "abc"))
	assert.NoError(t, validateName("a-b-c", "a-b-c"))
}

func TestValidateName_Invalid(t *testing.T) {
	assert.Error(t, validateName("", "dir"))
	assert.Error(t, validateName("Test", "Test"))             // uppercase
	assert.Error(t, validateName("test_skill", "test_skill")) // underscore
	assert.Error(t, validateName("test", "other-dir"))        // name != dir
	assert.Error(t, validateName("-test", "-test"))           // leading dash
}

func TestValidateName_TooLong(t *testing.T) {
	long := ""
	for i := 0; i < 65; i++ {
		long += "a"
	}
	assert.Error(t, validateName(long, long))
}

// ------------------------------------------------------------------ description validation

func TestValidateDescription_Valid(t *testing.T) {
	assert.NoError(t, validateDescription("A valid description"))
}

func TestValidateDescription_Invalid(t *testing.T) {
	assert.Error(t, validateDescription(""))
	long := ""
	for i := 0; i < 1025; i++ {
		long += "a"
	}
	assert.Error(t, validateDescription(long))
}

// ------------------------------------------------------------------ discovery helpers

func setupSkillDir(t *testing.T, workspace, name, content string) {
	t.Helper()
	dir := filepath.Join(workspace, projectSkillsDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFileName), []byte(content), 0o644))
}

// ------------------------------------------------------------------ discovery tests

func TestDiscover_Simple(t *testing.T) {
	workspace := t.TempDir()
	setupSkillDir(t, workspace, "test-skill",
		"---\nname: test-skill\ndescription: A test skill\n---\nSkill body here.")

	svc := NewServiceWithSources()
	skills, err := svc.Discover(workspace)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "test-skill", skills[0].Name)
	assert.Equal(t, "A test skill", skills[0].Description)
	assert.Equal(t, "project", skills[0].Source)
	assert.Equal(t, "Skill body here.", skills[0].Body)
}

func TestDiscover_SkillDirSubstitution(t *testing.T) {
	workspace := t.TempDir()
	setupSkillDir(t, workspace, "sub-test",
		"---\nname: sub-test\ndescription: desc\n---\nPath: ${SKILL_DIR}/data.json")

	svc := NewServiceWithSources()
	skills, err := svc.Discover(workspace)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Contains(t, skills[0].Body, filepath.Join(workspace, projectSkillsDir, "sub-test"))
	assert.NotContains(t, skills[0].Body, "${SKILL_DIR}")
}

func TestDiscover_Precedence_ProjectOverBuiltin(t *testing.T) {
	projectWs := t.TempDir()

	// Project skill (highest precedence).
	setupSkillDir(t, projectWs, "my-skill",
		"---\nname: my-skill\ndescription: Project version\n---\nProject body")

	// Same skill in builtin (lowest precedence).
	builtinFS := fstest.MapFS{
		"my-skill/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: my-skill\ndescription: Builtin version\n---\nBuiltin body"),
		},
	}

	svc := NewServiceWithSources(BuiltinSource{FS: builtinFS})
	skills, err := svc.Discover(projectWs)
	require.NoError(t, err)

	var found *Skill
	for i := range skills {
		if skills[i].Name == "my-skill" {
			found = &skills[i]
			break
		}
	}
	require.NotNil(t, found)
	// Project should win over builtin.
	assert.Equal(t, "Project version", found.Description)
	assert.Equal(t, "project", found.Source)
}

func TestDiscover_InvalidSkillSkipped(t *testing.T) {
	workspace := t.TempDir()
	setupSkillDir(t, workspace, "good-skill",
		"---\nname: good-skill\ndescription: Valid\n---\nBody")
	setupSkillDir(t, workspace, "bad-skill",
		"---\nname: wrong-name\ndescription: Invalid\n---\nBody")

	svc := NewServiceWithSources()
	skills, err := svc.Discover(workspace)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "good-skill", skills[0].Name)
}

func TestDiscover_EmptyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	svc := NewServiceWithSources()
	skills, err := svc.Discover(workspace)
	require.NoError(t, err)
	assert.Empty(t, skills)
}

func TestDiscover_OpaqueMetadata(t *testing.T) {
	workspace := t.TempDir()
	setupSkillDir(t, workspace, "meta-skill",
		"---\nname: meta-skill\ndescription: Has metadata\nmax_steps: 10\nblocked_sql:\n  - DROP TABLE\n---\nBody")

	svc := NewServiceWithSources()
	skills, err := svc.Discover(workspace)
	require.NoError(t, err)
	require.Len(t, skills, 1)

	assert.Equal(t, 10, skills[0].Frontmatter["max_steps"])
	blockedSQL, ok := skills[0].Frontmatter["blocked_sql"].([]any)
	assert.True(t, ok)
	assert.Equal(t, "DROP TABLE", blockedSQL[0])
}

// ------------------------------------------------------------------ builtin embed discovery

func TestDiscover_BuiltinEmbed(t *testing.T) {
	builtinFS := fstest.MapFS{
		"embed-skill/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: embed-skill\ndescription: An embedded skill\n---\nEmbedded body."),
		},
	}

	svc := NewServiceWithSources(BuiltinSource{FS: builtinFS})
	skills, err := svc.Discover(t.TempDir())
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "embed-skill", skills[0].Name)
	assert.Equal(t, "builtin", skills[0].Source)
	assert.Equal(t, "Embedded body.", skills[0].Body)
	assert.Contains(t, skills[0].Location, "builtin://")
}

func TestDiscover_BuiltinSources(t *testing.T) {
	coreFS := fstest.MapFS{
		"core-skill/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: core-skill\ndescription: Core skill\n---\nCore body."),
		},
	}
	dorisFS := fstest.MapFS{
		"health-check/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: health-check\ndescription: Doris health skill\n---\nDoris body."),
		},
	}

	svc := NewServiceWithSources(
		BuiltinSource{Name: "core", FS: coreFS},
		BuiltinSource{Name: "doris", FS: dorisFS},
	)
	skills, err := svc.Discover(t.TempDir())
	require.NoError(t, err)
	require.Len(t, skills, 2)

	locations := make(map[string]string, len(skills))
	for _, skill := range skills {
		locations[skill.Name] = skill.Location
	}
	assert.Equal(t, "builtin://core/core-skill/SKILL.md", locations["core-skill"])
	assert.Equal(t, "builtin://doris/health-check/SKILL.md", locations["health-check"])
}

func TestDiscover_BuiltinSourcePrecedence(t *testing.T) {
	coreFS := fstest.MapFS{
		"shared/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: shared\ndescription: Core version\n---\nCore body."),
		},
	}
	dorisFS := fstest.MapFS{
		"shared/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: shared\ndescription: Doris version\n---\nDoris body."),
		},
	}

	svc := NewServiceWithSources(
		BuiltinSource{Name: "core", FS: coreFS},
		BuiltinSource{Name: "doris", FS: dorisFS},
	)
	skills, err := svc.Discover(t.TempDir())
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "Core version", skills[0].Description)
	assert.Equal(t, "builtin://core/shared/SKILL.md", skills[0].Location)
}

func TestDiscover_BuiltinAllowlist(t *testing.T) {
	builtinFS := fstest.MapFS{
		"alpha/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: alpha\ndescription: Alpha skill\n---\nAlpha body."),
		},
		"beta/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: beta\ndescription: Beta skill\n---\nBeta body."),
		},
	}

	svc := NewServiceWithSources(BuiltinSource{FS: builtinFS, Allow: map[string]bool{"BETA": true}})
	skills, err := svc.Discover(t.TempDir())
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "beta", skills[0].Name)
	assert.Equal(t, "builtin", skills[0].Source)
}

func TestBuildActivation_FromFrontmatter(t *testing.T) {
	act := BuildActivation("slow-query", map[string]any{
		"name":        "slow-query",
		"description": "Analyze slow queries",
		"max_steps":   7,
		"blocked_sql": []any{"DROP TABLE", "TRUNCATE"},
		"mode":        "safe",
	})

	assert.Equal(t, "slow-query", act.Name)
	require.NotNil(t, act.MaxStepsOverride)
	assert.Equal(t, 7, *act.MaxStepsOverride)
	assert.JSONEq(t, `["DROP TABLE","TRUNCATE"]`, string(act.Metadata["blocked_sql"]))
	assert.JSONEq(t, `"safe"`, string(act.Metadata["mode"]))
	_, exists := act.Metadata["name"]
	assert.False(t, exists)
}

// ------------------------------------------------------------------ prompt rendering

func TestRenderPrompt_Empty(t *testing.T) {
	svc := NewServiceWithSources()
	assert.Equal(t, "", svc.RenderPrompt(nil, nil))
}

func TestRenderPrompt_NoActivated(t *testing.T) {
	svc := NewServiceWithSources()
	skills := []Skill{
		{Name: "alpha", Description: "Alpha desc"},
		{Name: "beta", Description: "Beta desc"},
	}
	prompt := svc.RenderPrompt(skills, nil)

	assert.Contains(t, prompt, "<available_skills>")
	assert.Contains(t, prompt, "- alpha: Alpha desc")
	assert.Contains(t, prompt, "- beta: Beta desc")
	assert.Contains(t, prompt, "</available_skills>")
	assert.NotContains(t, prompt, "active_skill_instructions")
}

func TestRenderPrompt_WithActivated(t *testing.T) {
	svc := NewServiceWithSources()
	skills := []Skill{
		{Name: "alpha", Description: "Alpha desc", Body: "Alpha instructions"},
		{Name: "beta", Description: "Beta desc", Body: "Beta instructions"},
	}
	activated := map[string]session.Activation{
		"alpha": {Name: "alpha"},
	}
	prompt := svc.RenderPrompt(skills, activated)

	assert.Contains(t, prompt, "<available_skills>")
	assert.Contains(t, prompt, `<active_skill_instructions name="alpha">`)
	assert.Contains(t, prompt, "Follow these instructions as your workflow guide")
	assert.Contains(t, prompt, "Alpha instructions")
	assert.NotContains(t, prompt, `<active_skill_instructions name="beta">`)
}

// ------------------------------------------------------------------ ResolveHints

func TestResolveHints(t *testing.T) {
	svc := NewServiceWithSources()

	hints := svc.ResolveHints("load $my-skill and $debug-it")
	assert.Equal(t, []string{"my-skill", "debug-it"}, hints)
}

func TestResolveHints_Empty(t *testing.T) {
	svc := NewServiceWithSources()
	hints := svc.ResolveHints("no hints here")
	assert.Empty(t, hints)
}

func TestResolveHints_InvalidName(t *testing.T) {
	svc := NewServiceWithSources()
	// underscore and leading dash are invalid per namePattern
	hints := svc.ResolveHints("load $_bad and $-wrong")
	assert.Empty(t, hints)
}

func TestResolveHints_AtPrefixIgnored(t *testing.T) {
	svc := NewServiceWithSources()
	// @ prefix should NOT be treated as skill hint (design uses $).
	hints := svc.ResolveHints("load @my-skill")
	assert.Empty(t, hints)
}

func TestResolveHints_WithPunctuation(t *testing.T) {
	svc := NewServiceWithSources()
	hints := svc.ResolveHints("请执行($health-check)，然后补一个 $slow-query。")
	assert.Equal(t, []string{"health-check", "slow-query"}, hints)
}

func TestResolveHints_IgnoresInlineDollarWithoutBoundary(t *testing.T) {
	svc := NewServiceWithSources()
	hints := svc.ResolveHints("abc$health-check and foo$slow-query")
	assert.Empty(t, hints)
}
