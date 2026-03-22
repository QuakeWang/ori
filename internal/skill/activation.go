package skill

import (
	"encoding/json"

	"github.com/QuakeWang/ori/internal/session"
)

// BuildActivation converts skill frontmatter into a session activation.
func BuildActivation(name string, frontmatter map[string]any) session.Activation {
	act := session.Activation{Name: name}
	if frontmatter == nil {
		return act
	}

	if maxSteps, ok := frontmatter["max_steps"]; ok {
		if value, ok := frontmatterInt(maxSteps); ok {
			act.MaxStepsOverride = &value
		}
	}

	metadata := make(map[string]json.RawMessage)
	for key, value := range frontmatter {
		if key == "name" || key == "description" || key == "max_steps" {
			continue
		}
		payload, err := json.Marshal(value)
		if err != nil {
			continue
		}
		metadata[key] = payload
	}
	if len(metadata) > 0 {
		act.Metadata = metadata
	}

	return act
}

func frontmatterInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		if number == float64(int(number)) {
			return int(number), true
		}
	}
	return 0, false
}
