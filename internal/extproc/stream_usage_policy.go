package extproc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

const streamUsagePolicyHeader = "x-aiplatform-stream-usage-policy"

type streamUsagePolicy struct {
	Enabled bool `json:"enabled"`
}

func streamUsagePolicyForModel(config *filterapi.RuntimeConfig, model string) (string, error) {
	if config == nil || model == "" {
		return "", nil
	}
	var selected string
	for _, backend := range config.Backends {
		if backend == nil || backend.Backend == nil || backend.Backend.ModelNameOverride != model {
			continue
		}
		mutation := backend.Backend.HeaderMutation
		if mutation == nil {
			continue
		}
		for _, header := range mutation.Set {
			if !strings.EqualFold(header.Name, streamUsagePolicyHeader) || header.Value == "" {
				continue
			}
			if selected != "" && selected != header.Value {
				return "", fmt.Errorf("conflicting stream usage policies for backend model %q", model)
			}
			selected = header.Value
		}
	}
	return selected, nil
}

// normalizeStreamUsage enables the final OpenAI-compatible SSE usage chunk
// only when the trusted shape policy requests it. A disabled or absent policy
// preserves the client's stream_options unchanged.
func normalizeStreamUsage(header string, body []byte) ([]byte, bool, error) {
	if header == "" {
		return body, false, nil
	}
	var policy streamUsagePolicy
	if err := json.Unmarshal([]byte(header), &policy); err != nil {
		return nil, false, fmt.Errorf("invalid stream usage policy: %w", err)
	}
	if !policy.Enabled {
		return body, false, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return nil, false, fmt.Errorf("invalid request body for stream usage policy: %w", err)
	}
	stream, _ := request["stream"].(bool)
	if !stream {
		return body, false, nil
	}

	options, exists := request["stream_options"]
	streamOptions, ok := options.(map[string]any)
	if exists && !ok {
		return nil, false, fmt.Errorf("stream_options must be an object")
	}
	if streamOptions == nil {
		streamOptions = map[string]any{}
		request["stream_options"] = streamOptions
	}
	if include, _ := streamOptions["include_usage"].(bool); include {
		return body, false, nil
	}
	streamOptions["include_usage"] = true
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("encode stream usage request: %w", err)
	}
	return normalized, true, nil
}
