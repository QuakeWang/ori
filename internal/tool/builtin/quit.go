package builtin

import (
	"context"
	"encoding/json"

	"github.com/QuakeWang/ori/internal/tool"
)

func quitTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "quit",
			Description: "Quit the current session.",
			Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			// The agent loop (Step 5) will check this result to break.
			return &tool.Result{
				Text: "Session tasks stopped.",
				Meta: map[string]any{"quit": true},
			}, nil
		},
	}
}
