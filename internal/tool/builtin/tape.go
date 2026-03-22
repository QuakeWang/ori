package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

type tapeHandoffInput struct {
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
}

func tapeInfoTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "tape.info",
			Description: "Get information about the current tape, such as number of entries and anchors.",
			Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			st, err := requireStore(tc)
			if err != nil {
				return nil, err
			}
			info, err := st.Info(tc.SessionID)
			if err != nil {
				return nil, err
			}
			text := fmt.Sprintf(
				"name: %s\nentries: %d\nanchors: %d\nlast_anchor: %s\nentries_since_last_anchor: %d\nlast_token_usage: %d",
				info.SessionID,
				info.Entries,
				info.Anchors,
				info.LastAnchor,
				info.EntriesSinceLastAnchor,
				info.LastTokenUsage,
			)
			return &tool.Result{Text: text}, nil
		},
	}
}

func tapeResetTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "tape.reset",
			Description: "Reset the current tape.",
			Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
			Dangerous:   true,
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			st, err := requireStore(tc)
			if err != nil {
				return nil, err
			}

			if err := st.Reset(tc.SessionID); err != nil {
				return nil, err
			}
			resetSessionState(tc.State, tc.SessionID, tc.Workspace)
			return &tool.Result{
				Text: fmt.Sprintf("tape reset: %s", tc.SessionID),
				Meta: map[string]any{"skip_state_save": true, "reset": true},
			}, nil
		},
	}
}

func tapeHandoffTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "tape.handoff",
			Description: "Add a handoff anchor to the current tape.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Anchor name"},"summary":{"type":"string","description":"Short summary for the anchor state"}}}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			st, err := requireStore(tc)
			if err != nil {
				return nil, err
			}

			var in tapeHandoffInput
			if err := decodeOptionalToolInput(input, &in, "tape.handoff"); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Name) == "" {
				in.Name = "handoff"
			}

			if err := st.AddAnchorWithSummary(tc.SessionID, in.Name, strings.TrimSpace(in.Summary), nil); err != nil {
				return nil, err
			}
			return &tool.Result{Text: fmt.Sprintf("anchor added: %s", in.Name)}, nil
		},
	}
}

func tapeAnchorsTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "tape.anchors",
			Description: "List anchors in the current tape.",
			Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			st, err := requireStore(tc)
			if err != nil {
				return nil, err
			}
			entries, err := st.List(tc.SessionID, 0)
			if err != nil {
				return nil, err
			}

			var anchors []string
			for _, entry := range entries {
				if entry.Kind != store.KindAnchor {
					continue
				}
				var payload store.AnchorPayload
				if err := json.Unmarshal(entry.Payload, &payload); err != nil {
					continue
				}
				anchors = append(anchors, payload.Name)
			}
			if len(anchors) == 0 {
				return &tool.Result{Text: "(no anchors)"}, nil
			}

			lines := make([]string, 0, len(anchors))
			for _, name := range anchors {
				lines = append(lines, "- "+name)
			}
			return &tool.Result{Text: strings.Join(lines, "\n")}, nil
		},
	}
}

func requireStore(tc *tool.Context) (store.Store, error) {
	if tc == nil || tc.Store == nil {
		return nil, fmt.Errorf("no tape store available in tool context")
	}
	return tc.Store, nil
}

func resetSessionState(state *session.State, sessionID, workspace string) {
	if state == nil {
		return
	}
	fresh := session.NewState(sessionID, workspace)
	state.SessionID = fresh.SessionID
	state.Workspace = fresh.Workspace
	state.ActivatedSkills = fresh.ActivatedSkills
	state.AllowedTools = fresh.AllowedTools
	state.AllowedSkills = fresh.AllowedSkills
	state.Extras = fresh.Extras
}
