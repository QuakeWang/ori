package doris

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/QuakeWang/ori/internal/skill"
)

//go:embed skills
var builtinSkillsFS embed.FS

// BuiltinSkillSource exposes Doris-owned builtin skills as an extension source.
func BuiltinSkillSource() (skill.BuiltinSource, error) {
	skillsFS, err := fs.Sub(builtinSkillsFS, "skills")
	if err != nil {
		return skill.BuiltinSource{}, fmt.Errorf("doris: sub builtin skills FS: %w", err)
	}
	return skill.BuiltinSource{
		Name: "doris",
		FS:   skillsFS,
	}, nil
}
