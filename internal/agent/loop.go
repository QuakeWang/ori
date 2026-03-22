package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

const (
	// continuePrompt is the default message injected into the LLM request
	// after tool execution to prompt the model to continue.
	continuePrompt = "Continue the task."
)

// ErrMaxStepsReached is returned when the agent loop hits the step limit.
var ErrMaxStepsReached = fmt.Errorf("agent: max steps reached")

// loop executes the core agent runtime loop:
// persist user input → reduce context → build prompt → LLM → tool calls → repeat.
func (a *Agent) loop(
	ctx context.Context,
	sess *session.Session,
	st store.Store,
	input llm.Input,
	opts RunOptions,
) (*RunResult, error) {
	if a.llm == nil {
		if a.llmInitErr != nil {
			return nil, fmt.Errorf("llm unavailable: %w", a.llmInitErr)
		}
		return nil, fmt.Errorf("llm unavailable: no client configured")
	}

	userMsg := inputToMessage(input)
	if err := st.Append(sess.ID, store.NewUserEntry(userMsg)); err != nil {
		return nil, fmt.Errorf("persist user entry: %w", err)
	}

	// Only activate hints that pass the AllowedSkills whitelist (if set).
	hintText := input.Text
	if hintText == "" && input.IsMultimodal() {
		hintText = extractTextFromParts(input.Parts)
	}
	discoveredSkills, discoverErr := a.discoverSkills(sess.State.Workspace)
	if discoverErr != nil {
		slog.Warn("agent.skill_discovery_failed",
			"workspace", sess.State.Workspace,
			"phase", "turn",
			"error", discoverErr)
	}

	maxSteps := a.maxStepsForState(sess.State)

	systemPrompt := a.buildSystemPrompt(sess.State, opts, discoveredSkills, hintText)
	prevActivated := activationSignature(sess.State)

	result := &RunResult{SessionID: sess.ID}

	for step := 1; step <= maxSteps; step++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		stepStart := time.Now()

		entries, err := st.List(sess.ID, 0)
		if err != nil {
			return nil, fmt.Errorf("load entries: %w", err)
		}
		messages, err := a.reducer.Reduce(entries)
		if err != nil {
			return nil, fmt.Errorf("reduce context: %w", err)
		}

		// Inject ephemeral continue prompt after tool execution rounds.
		// This is NOT persisted to avoid accumulating noise entries.
		if step > 1 {
			messages = append(messages, llm.Message{Role: "user", Content: continuePrompt})
		}

		model := opts.Model
		if model == "" {
			model = a.settings.Model
		}

		toolFilter := a.toolFilter(opts, sess.State)
		schemas := a.tools.Schemas(toolFilter)

		req := llm.Request{
			Model:     model,
			System:    systemPrompt,
			Messages:  messages,
			Tools:     schemas,
			MaxTokens: a.settings.MaxTokens,
		}
		if a.settings.ModelTimeout > 0 {
			req.Timeout = a.settings.ModelTimeout
		}

		resp, err := a.llm.Chat(ctx, req)
		if err != nil {
			a.recordStepEvent(st, sess.ID, step, stepStart, "error", err.Error())
			return nil, fmt.Errorf("llm.chat (step %d): %w", step, err)
		}

		if len(resp.ToolCalls) > 0 {
			// Persist tool_call entry.
			tcEntry, err := store.NewToolCallMessageEntry(llm.Message{
				Role:      "assistant",
				Content:   resp.Text,
				ToolCalls: resp.ToolCalls,
			})
			if err != nil {
				return nil, fmt.Errorf("create tool_call entry: %w", err)
			}
			if err := st.Append(sess.ID, tcEntry); err != nil {
				return nil, fmt.Errorf("persist tool_call entry: %w", err)
			}

			// Execute tools sequentially.
			resultItems := make([]store.ToolResultItem, 0, len(resp.ToolCalls))
			quitRequested := false
			quitText := ""
			resetRequested := false
			for _, tc := range resp.ToolCalls {
				// Translate model name to external name for execution.
				extName := tool.ExternalName(tc.Name)
				toolCtx := a.newToolContext(sess, st, discoveredSkills)

				toolResult, toolErr := a.tools.Execute(ctx, toolCtx, extName, tc.Arguments, toolFilter)
				content := ""
				if toolErr != nil {
					content = fmt.Sprintf("Error: %v", toolErr)
					slog.Warn("agent.tool_error", "tool", extName, "error", toolErr)
				} else if toolResult != nil {
					content = toolResult.Text
					if metaFlag(toolResult.Meta, "quit") {
						quitRequested = true
						if quitText == "" {
							quitText = toolResult.Text
						}
					}
					if metaFlag(toolResult.Meta, "reset") {
						resetRequested = true
						break // Stop batch: remaining tools must not execute.
					}
				}

				resultItems = append(resultItems, store.ToolResultItem{
					ToolCallID: tc.ID,
					Name:       extName,
					Content:    content,
				})
			}

			// After tape.reset, the store has been cleared. Do NOT write
			// tool_result/event/state — return immediately so the tape
			// stays clean. RunTurn will re-seed the bootstrap anchor.
			if resetRequested {
				result.Output = "tape reset"
				result.Steps = step
				result.Meta = map[string]any{"reset": true, "skip_state_save": true}
				return result, nil
			}

			// Persist tool_result entry.
			trEntry, err := store.NewToolResultEntry(resultItems)
			if err != nil {
				return nil, fmt.Errorf("create tool_result entry: %w", err)
			}
			if err := st.Append(sess.ID, trEntry); err != nil {
				return nil, fmt.Errorf("persist tool_result entry: %w", err)
			}

			// If skill activation changed, rebuild system prompt.
			curActivated := activationSignature(sess.State)
			if curActivated != prevActivated {
				systemPrompt = a.buildSystemPrompt(sess.State, opts, discoveredSkills, hintText)
				prevActivated = curActivated
				maxSteps = a.maxStepsForState(sess.State)
			}

			if quitRequested {
				a.recordStepEvent(st, sess.ID, step, stepStart, "quit", "")
				result.Output = quitText
				result.Steps = step
				result.Meta = map[string]any{"quit": true}
				return result, nil
			}

			// Continue prompt is injected ephemerally at the next iteration
			// (see step > 1 check above), NOT persisted.

			a.recordStepEvent(st, sess.ID, step, stepStart, "continue", "")
			result.Steps = step
			continue
		}

		assistantMsg := llm.Message{
			Role:    "assistant",
			Content: resp.Text,
		}
		assistantEntry := store.NewAssistantEntry(assistantMsg, resp.FinishReason, resp.Usage)
		if err := st.Append(sess.ID, assistantEntry); err != nil {
			return nil, fmt.Errorf("persist assistant entry: %w", err)
		}

		a.recordStepEvent(st, sess.ID, step, stepStart, "ok", "")
		result.Output = resp.Text
		result.Steps = step
		return result, nil
	}

	result.Steps = maxSteps
	return result, ErrMaxStepsReached
}

// recordStepEvent writes a loop.step event to the store for observability.
// Best-effort: errors are logged but do not interrupt the agent loop.
func (a *Agent) recordStepEvent(st store.Store, sessionID string, step int, start time.Time, status, errMsg string) {
	payload := map[string]any{
		"name":       "loop.step",
		"step":       step,
		"elapsed_ms": time.Since(start).Milliseconds(),
		"status":     status,
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	if err := st.Append(sessionID, store.NewEventEntry(payload)); err != nil {
		slog.Warn("agent.record_step_event", "error", err)
	}
}

func (a *Agent) discoverSkills(workspace string) ([]skill.Skill, error) {
	if a.skills == nil {
		return nil, nil
	}
	return a.skills.Discover(workspace)
}

func indexSkillsByName(skills []skill.Skill) map[string]skill.Skill {
	index := make(map[string]skill.Skill, len(skills))
	for _, item := range skills {
		index[strings.ToLower(item.Name)] = item
	}
	return index
}

func (a *Agent) maxStepsForState(state *session.State) int {
	maxSteps := a.settings.MaxSteps
	for _, act := range state.ActivatedSkills {
		if act.MaxStepsOverride != nil && *act.MaxStepsOverride > maxSteps {
			maxSteps = *act.MaxStepsOverride
		}
	}
	return maxSteps
}

func activationSignature(state *session.State) string {
	payload, err := json.Marshal(state.ActivatedSkills)
	if err != nil {
		return fmt.Sprintf("activation-count:%d", len(state.ActivatedSkills))
	}
	return string(payload)
}

// inputToMessage converts an llm.Input to an llm.Message.
func inputToMessage(input llm.Input) llm.Message {
	msg := llm.Message{Role: "user"}
	if input.IsMultimodal() {
		msg.Parts = input.Parts
	} else {
		msg.Content = input.Text
	}
	return msg
}

// extractTextFromParts concatenates all text-type parts for hint resolution.
func extractTextFromParts(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// toolFilter builds a tool.Filter from run options and session state.
func (a *Agent) toolFilter(opts RunOptions, state *session.State) tool.Filter {
	// Options take priority ([]string → map).
	// nil = not specified; non-nil (incl. empty) = explicit boundary.
	if allowed := session.AllowlistFromSlice(opts.AllowedTools); allowed != nil {
		return tool.Filter{AllowedTools: allowed}
	}
	// Fall back to session state (already a map, nil = no restriction).
	if state.AllowedTools != nil {
		return tool.Filter{AllowedTools: state.AllowedTools}
	}
	// No filter: all non-dangerous tools are visible.
	return tool.Filter{}
}
