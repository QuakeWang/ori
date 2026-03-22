package builtin

import (
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/tool"
)

// RegisterCore registers the core builtin tools into the given registry.
// Doris tools are assembled outside this package so the runtime core keeps
// a clean extension boundary.
func RegisterCore(r *tool.Registry, svc *skill.Service) error {
	tools := []*tool.Tool{
		// Core tools
		helpTool(r),
		bashTool(),
		bashOutputTool(),
		bashKillTool(),
		fsReadTool(),
		fsWriteTool(),
		fsEditTool(),
		webFetchTool(),
		quitTool(),

		// Skill tool (real implementation)
		skillTool(svc),

		// Tape tools backed by the JSONL store.
		tapeInfoTool(),
		tapeResetTool(),
		tapeHandoffTool(),
		tapeAnchorsTool(),

		// Subagent stub — real handler is injected by bootstrap.go via Replace.
		subagentStub(),
	}

	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	return nil
}
