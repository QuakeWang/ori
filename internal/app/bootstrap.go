package app

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/QuakeWang/ori/internal/agent"
	"github.com/QuakeWang/ori/internal/config"
	"github.com/QuakeWang/ori/internal/doris"
	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
	"github.com/QuakeWang/ori/internal/tool/builtin"
)

//go:embed skills
var coreBuiltinSkillsFS embed.FS

// Build creates a fully wired Agent from configuration.
func Build(cfg *config.Settings, workspace string) (*agent.Agent, error) {

	var llmInitErr error
	llmClient, err := llm.NewProviderRouter(cfg)
	if err != nil {
		llmInitErr = fmt.Errorf("app: create llm client: %w", err)
	}

	registry, skillSvc, err := buildShared()
	if err != nil {
		return nil, err
	}

	storeDir := filepath.Join(cfg.Home, "store")
	st, err := store.NewJSONLStore(storeDir, workspace)
	if err != nil {
		return nil, fmt.Errorf("app: create store: %w", err)
	}

	reducer := &store.DefaultReducer{
		WindowSize:            cfg.ContextWindowSize,
		MaxToolResultLen:      cfg.ContextMaxToolResult,
		MaxToolResultInWindow: cfg.ContextMaxToolResultInWindow,
	}

	// Command-mode turns can still run without an LLM.
	// client; non-command turns will surface llmInitErr at runtime.
	ag := agent.New(cfg, llmClient, registry, skillSvc, st, reducer, workspace, llmInitErr)

	// Register subagent tool with a closure that calls back into the agent.
	// This replaces the stub registered by RegisterCore.
	builtin.RegisterSubagent(registry, ag.RunSubagent)

	return ag, nil
}

// ReadOnlyComponents holds the components needed for read-only CLI commands
// (ori tools, ori skills) that don't need LLM or store.
type ReadOnlyComponents struct {
	Registry *tool.Registry
	Skills   *skill.Service
}

// BuildReadOnly creates only the tool registry and skill service,
// without requiring LLM provider configuration or store directory.
// Suitable for ori tools and ori skills commands.
func BuildReadOnly() (*ReadOnlyComponents, error) {
	registry, skillSvc, err := buildShared()
	if err != nil {
		return nil, err
	}
	return &ReadOnlyComponents{
		Registry: registry,
		Skills:   skillSvc,
	}, nil
}

// buildShared creates the tool registry and skill service that are
// shared between Build and BuildReadOnly.
func buildShared() (*tool.Registry, *skill.Service, error) {
	registry := tool.NewRegistry()

	builtinSources, err := builtinSkillSources()
	if err != nil {
		return nil, nil, err
	}
	skillSvc := skill.NewServiceWithSources(builtinSources...)

	if err := builtin.RegisterCore(registry, skillSvc); err != nil {
		return nil, nil, fmt.Errorf("app: register builtin tools: %w", err)
	}
	if err := doris.RegisterTools(registry); err != nil {
		return nil, nil, fmt.Errorf("app: register doris tools: %w", err)
	}

	return registry, skillSvc, nil
}

func builtinSkillSources() ([]skill.BuiltinSource, error) {
	coreSource, err := coreBuiltinSkillSource()
	if err != nil {
		return nil, err
	}
	dorisSource, err := doris.BuiltinSkillSource()
	if err != nil {
		return nil, fmt.Errorf("app: doris builtin skill source: %w", err)
	}
	return []skill.BuiltinSource{coreSource, dorisSource}, nil
}

func coreBuiltinSkillSource() (skill.BuiltinSource, error) {
	skillsFS, err := fs.Sub(coreBuiltinSkillsFS, "skills")
	if err != nil {
		return skill.BuiltinSource{}, fmt.Errorf("app: sub core builtin skills FS: %w", err)
	}
	return skill.BuiltinSource{
		Name: "core",
		FS:   skillsFS,
	}, nil
}
