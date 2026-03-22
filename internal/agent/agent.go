package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/QuakeWang/ori/internal/config"
	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

// Agent is the core runtime that connects LLM, tools, skills, and store.
type Agent struct {
	llm        llm.Client
	llmInitErr error
	tools      *tool.Registry
	skills     *skill.Service
	store      store.Store
	reducer    store.Reducer
	settings   *config.Settings
	workspace  string
}

// RunOptions configures a single agent run.
type RunOptions struct {
	Model         string
	AllowedTools  []string
	AllowedSkills []string
}

// RunResult holds the outcome of an agent run.
type RunResult struct {
	SessionID string
	Output    string
	Steps     int
	Meta      map[string]any
}

// New creates a new Agent with the given dependencies.
// workspace is the project root directory that controls AGENTS.md lookup,
// skill discovery, and tool working directory.
func New(
	cfg *config.Settings,
	llmClient llm.Client,
	tools *tool.Registry,
	skills *skill.Service,
	st store.Store,
	reducer store.Reducer,
	workspace string,
	llmInitErr error,
) *Agent {
	return &Agent{
		llm:        llmClient,
		llmInitErr: llmInitErr,
		tools:      tools,
		skills:     skills,
		store:      st,
		reducer:    reducer,
		settings:   cfg,
		workspace:  workspace,
	}
}

func (a *Agent) ensureBootstrapAnchor(st store.Store, sessionID string) error {
	entries, err := st.List(sessionID, 1)
	if err != nil {
		return fmt.Errorf("load bootstrap state: %w", err)
	}
	if len(entries) > 0 {
		return nil
	}
	if err := st.AddAnchor(sessionID, "session/start", nil); err != nil {
		return fmt.Errorf("create bootstrap anchor: %w", err)
	}
	return nil
}

// NewSession creates a session for use with RunTurn. If the store contains
// a previous state snapshot for this sessionID, it is restored. This ensures
// ActivatedSkills and allowlists persist across process restarts.
func (a *Agent) NewSession(sessionID string, opts RunOptions) *session.Session {
	sess := session.New(sessionID, a.workspace)
	sess.Store = a.store // default: agent-level store

	// Try to restore state from store (best-effort).
	entries, err := a.store.List(sessionID, 0)
	if err != nil {
		slog.Warn("agent.state_restore_failed", "session", sessionID, "error", err)
	} else if len(entries) > 0 {
		if sp := store.LoadLatestState(entries); sp != nil {
			applyStatePayload(sess.State, sp)
			slog.Info("agent.state_restored", "session", sessionID,
				"activated_skills", len(sp.ActivatedSkills))
		}
	}

	// Apply allowed tools/skills from options.
	// If RunOptions provides explicit allowlists, they REPLACE
	// (not merge with) any restored state to prevent permission drift.
	if m := session.AllowlistFromSlice(opts.AllowedTools); m != nil {
		sess.State.AllowedTools = m
	}
	if m := session.AllowlistFromSlice(opts.AllowedSkills); m != nil {
		sess.State.AllowedSkills = m
	}
	return sess
}

// RunTurn executes a single agent turn using an existing session.
// The session's runtime state is preserved across calls.
// After each turn, the state is persisted to the store.
func (a *Agent) RunTurn(
	ctx context.Context,
	sess *session.Session,
	input llm.Input,
	opts RunOptions,
) (*RunResult, error) {
	st := a.sessionStore(sess)
	if err := a.ensureBootstrapAnchor(st, sess.ID); err != nil {
		return nil, err
	}

	// Command mode: input starts with ","
	if !input.IsMultimodal() {
		trimmed := strings.TrimSpace(input.Text)
		if strings.HasPrefix(trimmed, ",") {
			result, err := a.runCommand(ctx, sess, st, trimmed[1:], opts)
			a.postTurn(sess, st, result)
			return result, err
		}
	}

	result, err := a.loop(ctx, sess, st, input, opts)
	a.postTurn(sess, st, result)
	if err != nil {
		return result, fmt.Errorf("agent.run: %w", err)
	}
	return result, nil
}

// postTurn handles state persistence and bootstrap anchor re-creation
// after a command or loop turn completes.
func (a *Agent) postTurn(sess *session.Session, st store.Store, result *RunResult) {
	meta := resultMeta(result)
	if !metaFlag(meta, "skip_state_save") {
		a.saveState(sess)
	}
	if metaFlag(meta, "reset") {
		_ = a.ensureBootstrapAnchor(st, sess.ID)
	}
}

// resultMeta returns the Meta map from a RunResult, or nil.
func resultMeta(result *RunResult) map[string]any {
	if result == nil {
		return nil
	}
	return result.Meta
}

// Run is a convenience wrapper that creates a fresh session and executes
// a single turn. Suitable for one-shot execution (ori run). For multi-turn
// chat, use NewSession + RunTurn instead.
func (a *Agent) Run(
	ctx context.Context,
	sessionID string,
	input llm.Input,
	opts RunOptions,
) (*RunResult, error) {
	sess := a.NewSession(sessionID, opts)
	return a.RunTurn(ctx, sess, input, opts)
}

// ListTools returns all tools passing the given filter.
func (a *Agent) ListTools(f tool.Filter) []*tool.Tool {
	return a.tools.List(f)
}

// SessionInfo returns persisted summary statistics for a session.
func (a *Agent) SessionInfo(sessionID string) (store.SessionInfo, error) {
	return a.store.Info(sessionID)
}

// SessionEntries returns all persisted entries for a session.
func (a *Agent) SessionEntries(sessionID string) ([]store.Entry, error) {
	return a.store.List(sessionID, 0)
}

// DefaultModel returns the configured default model name.
func (a *Agent) DefaultModel() string {
	if a.settings == nil {
		return ""
	}
	return a.settings.Model
}

// WrapStoreOverlay replaces the agent's store with an in-memory overlay.
// All subsequent writes go to the overlay; reads merge base + overlay.
func (a *Agent) WrapStoreOverlay() {
	a.store = store.NewOverlayStore(a.store)
}

// DiscardOverlay drops all pending overlay entries for a session.
// If the store is not an OverlayStore, this is a no-op.
func (a *Agent) DiscardOverlay(sessionID string) {
	if ov, ok := a.store.(*store.OverlayStore); ok {
		ov.Discard(sessionID)
	}
}

// RunSubagent executes a one-shot sub-agent turn with the given constraints.
// This is the implementation behind the subagent tool. The function signature
// matches builtin.SubagentRunner so it can be passed directly as a closure.
//
// Behavior:
//   - Temp sessions (prefix "temp/") get an OverlayStore on the session, so
//     entries are discarded after the sub-agent completes. No agent-level
//     state is mutated — this is concurrent-safe.
//   - parentState is used to inherit ActivatedSkills and Extras into the
//     sub-agent's session.
func (a *Agent) RunSubagent(
	ctx context.Context,
	sessionID string,
	prompt string,
	model string,
	allowedTools []string,
	allowedSkills []string,
	parentState *session.State,
) (string, error) {
	// No llmInitErr guard here: RunTurn supports ,command mode without LLM,
	// and loop() has its own LLM availability check.

	opts := RunOptions{
		Model:         model,
		AllowedTools:  a.buildSubagentToolList(allowedTools),
		AllowedSkills: allowedSkills,
	}

	// Create session and inherit parent state before running.
	sess := a.NewSession(sessionID, opts)
	inheritParentState(sess.State, parentState)

	// Temp session isolation: override session store with an ephemeral overlay.
	// This does NOT touch a.store — safe for concurrent subagent runs.
	if strings.HasPrefix(sessionID, "temp/") {
		sess.Store = store.NewOverlayStore(a.store)
	}

	result, err := a.RunTurn(ctx, sess, llm.Input{Text: prompt}, opts)
	if err != nil {
		if result != nil && result.Output != "" {
			return result.Output, err
		}
		return "", err
	}
	return result.Output, nil
}

// inheritParentState copies the parent's ActivatedSkills and Extras into the
// sub-agent's session. ActivatedSkills are filtered against the child's
// AllowedSkills whitelist (if set) to ensure a closed skill boundary.
func inheritParentState(child *session.State, parent *session.State) {
	if parent == nil || child == nil {
		return
	}
	hasSkillWhitelist := child.AllowedSkills != nil
	for k, v := range parent.ActivatedSkills {
		if _, exists := child.ActivatedSkills[k]; exists {
			continue
		}
		// Respect AllowedSkills: don't inherit skills outside the whitelist.
		if hasSkillWhitelist && !skill.Allowed(child.AllowedSkills, k) {
			continue
		}
		child.ActivatedSkills[k] = v
	}
	for k, v := range parent.Extras {
		if _, exists := child.Extras[k]; !exists {
			child.Extras[k] = v
		}
	}
}

// sessionStore returns the per-session store, falling back to the agent store.
func (a *Agent) sessionStore(sess *session.Session) store.Store {
	if sess.Store != nil {
		return sess.Store
	}
	return a.store
}

func (a *Agent) newToolContext(sess *session.Session, st store.Store, skills []skill.Skill) *tool.Context {
	return &tool.Context{
		SessionID: sess.ID,
		Workspace: sess.State.Workspace,
		State:     sess.State,
		Store:     st,
		Skills:    skills,
	}
}

// buildSubagentToolList ensures "subagent" is never available to a sub-agent,
// preventing infinite recursion. When an explicit list is provided (non-nil),
// it filters out "subagent". An explicit empty list signals deny-all.
// When no list is provided (nil), it uses the default LLM-mode policy
// (non-dangerous tools only, minus "subagent").
func (a *Agent) buildSubagentToolList(explicit []string) []string {
	if explicit != nil {
		return excludeToolName(explicit, "subagent")
	}
	// Default: same delegation policy as LLM mode — exclude dangerous tools.
	all := a.tools.List(tool.Filter{})
	result := make([]string, 0, len(all))
	for _, t := range all {
		result = append(result, t.Spec.Name)
	}
	return excludeToolName(result, "subagent")
}

func excludeToolName(tools []string, name string) []string {
	filtered := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != name {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// ------------------------------------------------------------------ state save/load helpers

// saveState persists the current session state to the session's store.
func (a *Agent) saveState(sess *session.Session) {
	st := a.sessionStore(sess)
	sp := buildStatePayload(sess.State)
	entry := store.NewStateEntry(sp)
	if err := st.Append(sess.ID, entry); err != nil {
		slog.Warn("agent.save_state_failed", "session", sess.ID, "error", err)
	}
}

// buildStatePayload converts session.State to a store.StatePayload.
func buildStatePayload(state *session.State) store.StatePayload {
	skills := make(map[string]store.ActivatedSkillState, len(state.ActivatedSkills))
	for k, v := range state.ActivatedSkills {
		skills[k] = store.ActivatedSkillState{
			Name:             v.Name,
			MaxStepsOverride: v.MaxStepsOverride,
			Metadata:         v.Metadata,
		}
	}
	return store.StatePayload{
		ActivatedSkills: skills,
		AllowedTools:    state.AllowedTools,
		AllowedSkills:   state.AllowedSkills,
		Extras:          state.Extras,
	}
}

// applyStatePayload restores a persisted state snapshot into a session.State.
func applyStatePayload(state *session.State, sp *store.StatePayload) {
	for k, v := range sp.ActivatedSkills {
		state.ActivatedSkills[k] = session.Activation{
			Name:             v.Name,
			MaxStepsOverride: v.MaxStepsOverride,
			Metadata:         v.Metadata,
		}
	}
	// Direct assignment: state starts with nil maps (= no restriction).
	// A non-nil payload restores the exact persisted policy.
	if sp.AllowedTools != nil {
		state.AllowedTools = sp.AllowedTools
	}
	if sp.AllowedSkills != nil {
		state.AllowedSkills = sp.AllowedSkills
	}
	for k, v := range sp.Extras {
		state.Extras[k] = v
	}
	skill.BackfillActivationRuntimeState(state)
}
