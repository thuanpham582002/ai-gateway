package extproc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// normalizeResponsesCompatibility bridges Responses clients that rely on
// documented text defaults to frontends whose request schema still requires
// those fields. Existing formats and every other request field are kept.
func normalizeResponsesCompatibility(body []byte) ([]byte, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return nil, false, fmt.Errorf("invalid Responses request: %w", err)
	}

	changed := false
	if text, ok := request["text"].(map[string]any); ok {
		if format, exists := text["format"]; !exists || format == nil {
			text["format"] = map[string]any{"type": "text"}
			changed = true
		}
	}
	if tools, ok := request["tools"].([]any); ok {
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok || tool["type"] != "custom" {
				continue
			}
			if format, exists := tool["format"]; exists && format != nil {
				continue
			}
			tool["format"] = map[string]any{"type": "text"}
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("encode Responses tool compatibility request: %w", err)
	}
	return normalized, true, nil
}
