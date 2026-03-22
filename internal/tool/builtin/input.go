package builtin

import (
	"encoding/json"
	"fmt"
)

func decodeToolInput(input json.RawMessage, target any, toolName string) error {
	if err := json.Unmarshal(input, target); err != nil {
		return fmt.Errorf("invalid %s input: %w", toolName, err)
	}
	return nil
}

func decodeOptionalToolInput(input json.RawMessage, target any, toolName string) error {
	if len(input) == 0 {
		return nil
	}
	return decodeToolInput(input, target, toolName)
}
