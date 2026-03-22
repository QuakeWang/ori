package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/QuakeWang/ori/internal/agent"
	"github.com/QuakeWang/ori/internal/app"
	"github.com/QuakeWang/ori/internal/config"
	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/tool"
)

const defaultSessionID = "cli:local"

type sessionRuntime struct {
	agent     *agent.Agent
	cfg       *config.Settings
	renderer  *Renderer
	workspace string
	sessionID string
	opts      agent.RunOptions
}

// RootCmd builds the ori CLI tree.
func RootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ori",
		Short: "Ori – AI agent runtime",
	}
	root.PersistentFlags().StringP("workspace", "w", "", "workspace directory")
	root.PersistentFlags().String("model", "", "override LLM model")
	root.AddCommand(runCmd(), chatCmd(), toolsCmd(), skillsCmd())
	return root
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [message]",
		Short: "Run a single agent turn",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, cleanup, err := bootstrapSessionRuntime(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := rt.agent.Run(cmd.Context(), rt.sessionID, llm.Input{Text: args[0]}, rt.opts)
			if err != nil {
				if result != nil && result.Output != "" {
					rt.renderer.Output("Error", result.Output)
				}
				return err
			}

			view := sessionView(rt.agent, rt.opts, rt.workspace, rt.sessionID)
			rt.renderer.WelcomeRun(view)
			kind := "Assistant"
			if strings.HasPrefix(strings.TrimSpace(args[0]), ",") {
				kind = "Command"
			}
			rt.renderer.Output(kind, result.Output)
			rt.renderer.SessionStatus(view)
			return nil
		},
	}
	addSessionFlags(cmd)
	addEphemeralFlag(cmd)
	return cmd
}

func chatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Start an interactive chat session",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, cleanup, err := bootstrapSessionRuntime(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			sess := rt.agent.NewSession(rt.sessionID, rt.opts)
			view := sessionView(rt.agent, rt.opts, rt.workspace, rt.sessionID)
			rt.renderer.WelcomeChat(view)

			hp := historyPath(rt.cfg.Home, rt.workspace)
			rp := newREPL(rt.agent, sess, rt.renderer, rt.opts, rt.workspace, rt.sessionID, hp)
			return rp.Run(cmd.Context())
		},
	}
	addSessionFlags(cmd)
	addEphemeralFlag(cmd)
	return cmd
}

func toolsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List registered tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := NewRenderer(cmd.OutOrStdout(), cmd.ErrOrStderr())
			ro, err := app.BuildReadOnly()
			if err != nil {
				return err
			}
			tools := ro.Registry.List(tool.Filter{IncludeAll: true})
			rows := make([][]string, len(tools))
			for i, t := range tools {
				rows[i] = []string{t.Spec.Name, truncate(t.Spec.Description, 60), fmt.Sprintf("%v", t.Spec.Dangerous)}
			}
			r.Table([]string{"NAME", "DESCRIPTION", "DANGEROUS"}, rows)
			return nil
		},
	}
}

func skillsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skills",
		Short: "List discovered skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := NewRenderer(cmd.OutOrStdout(), cmd.ErrOrStderr())
			workspace := workspaceFromCmd(cmd)
			ro, err := app.BuildReadOnly()
			if err != nil {
				return err
			}
			skills, err := ro.Skills.Discover(workspace)
			if err != nil {
				return err
			}
			rows := make([][]string, len(skills))
			for i, s := range skills {
				rows[i] = []string{s.Name, truncate(s.Description, 50), s.Source, truncate(s.Location, 40)}
			}
			r.Table([]string{"NAME", "DESCRIPTION", "SOURCE", "LOCATION"}, rows)
			return nil
		},
	}
}

// --- helpers ---

func bootstrap(cmd *cobra.Command) (*agent.Agent, *config.Settings, string, error) {
	workspace := workspaceFromCmd(cmd)
	cfg, err := config.Load(workspace)
	if err != nil {
		return nil, nil, "", fmt.Errorf("load config: %w", err)
	}
	if model, _ := cmd.Flags().GetString("model"); model != "" {
		cfg.Model = model
	}
	ConfigureLogging(cfg.Verbose)
	ag, err := app.Build(cfg, workspace)
	if err != nil {
		return nil, nil, "", fmt.Errorf("build agent: %w", err)
	}
	return ag, cfg, workspace, nil
}

func bootstrapSessionRuntime(cmd *cobra.Command) (*sessionRuntime, func(), error) {
	ag, cfg, workspace, err := bootstrap(cmd)
	if err != nil {
		return nil, nil, err
	}

	rt := &sessionRuntime{
		agent:     ag,
		cfg:       cfg,
		renderer:  NewRenderer(cmd.OutOrStdout(), cmd.ErrOrStderr()),
		workspace: workspace,
		sessionID: sessionID(cmd),
		opts:      runOpts(cmd),
	}

	cleanup := func() {}
	ephemeral, _ := cmd.Flags().GetBool("ephemeral")
	if ephemeral {
		ag.WrapStoreOverlay()
		cleanup = func() {
			ag.DiscardOverlay(rt.sessionID)
		}
	}

	return rt, cleanup, nil
}

func addEphemeralFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("ephemeral", false, "use in-memory overlay (discard on exit)")
}

func addSessionFlags(cmd *cobra.Command) {
	cmd.Flags().String("session-id", "", "explicit session ID (default: cli:local)")
}

func workspaceFromCmd(cmd *cobra.Command) string {
	workspace, _ := cmd.Flags().GetString("workspace")
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	return canonicalWorkspace(workspace)
}

func sessionID(cmd *cobra.Command) string {
	if sid, _ := cmd.Flags().GetString("session-id"); sid != "" {
		return sid
	}
	return defaultSessionID
}

func runOpts(cmd *cobra.Command) agent.RunOptions {
	model, _ := cmd.Flags().GetString("model")
	return agent.RunOptions{Model: model}
}

func sessionView(ag *agent.Agent, opts agent.RunOptions, workspace, sid string) SessionView {
	view := SessionView{SessionID: sid, Workspace: workspace, Model: opts.Model}
	if view.Model == "" {
		view.Model = ag.DefaultModel()
	}
	if info, err := ag.SessionInfo(sid); err == nil {
		view.Entries = info.Entries
		view.Anchors = info.Anchors
		view.LastAnchor = info.LastAnchor
		view.LastTokenUsage = info.LastTokenUsage
	}
	return view
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// canonicalWorkspace resolves symlinks and returns the absolute path.
// This ensures /repo, /repo/../repo, and symlink paths all map to
// the same canonical path for consistent session store keying.
func canonicalWorkspace(ws string) string {
	resolved, err := filepath.EvalSymlinks(ws)
	if err != nil {
		// Fall back to Abs if EvalSymlinks fails (e.g. path doesn't exist yet).
		abs, absErr := filepath.Abs(ws)
		if absErr != nil {
			return ws
		}
		return abs
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return resolved
	}
	return abs
}
