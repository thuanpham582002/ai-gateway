package extproc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

const reasoningPolicyHeader = "x-aiplatform-reasoning-policy"

// reasoningPolicyForModel resolves a shape-owned policy before endpoint
// selection. InferencePool selection happens after the router ext-proc filter,
// so validation cannot depend solely on the selected upstream worker.
func reasoningPolicyForModel(config *filterapi.RuntimeConfig, model string) (string, error) {
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
			if !strings.EqualFold(header.Name, reasoningPolicyHeader) || header.Value == "" {
				continue
			}
			if selected != "" && selected != header.Value {
				return "", fmt.Errorf("conflicting reasoning policies for backend model %q", model)
			}
			selected = header.Value
		}
	}
	return selected, nil
}

type reasoningPolicy struct {
	Enabled             bool   `json:"enabled"`
	DefaultTokenBudget  int64  `json:"defaultTokenBudget"`
	MaxTokenBudget      int64  `json:"maxTokenBudget"`
	AnswerReserveTokens int64  `json:"answerReserveTokens"`
	Transport           string `json:"transport"`
}

// normalizeReasoningBudget preserves an explicit client budget, supplies the
// shape default only when absent, and clamps the effective value to immutable
// shape/output limits. The trusted policy header is injected by route config.
func normalizeReasoningBudget(header string, body []byte) ([]byte, bool, error) {
	return normalizeReasoningBudgetAfterTranslation(header, body, body)
}

// normalizeReasoningBudgetAfterTranslation reads client-owned fields from the
// original request while mutating the translated backend request. Translation
// uses typed OpenAI structs and may otherwise discard vendor extension fields.
func normalizeReasoningBudgetAfterTranslation(header string, sourceBody, targetBody []byte) ([]byte, bool, error) {
	if header == "" {
		return targetBody, false, nil
	}
	var policy reasoningPolicy
	if err := json.Unmarshal([]byte(header), &policy); err != nil || !policy.Enabled || policy.MaxTokenBudget <= 0 {
		return nil, false, fmt.Errorf("invalid reasoning policy")
	}
	if policy.Transport != "openai" && policy.Transport != "dynamo_nvext" {
		return nil, false, fmt.Errorf("unsupported reasoning policy transport %q", policy.Transport)
	}
	decoder := json.NewDecoder(bytes.NewReader(sourceBody))
	decoder.UseNumber()
	var source map[string]any
	if err := decoder.Decode(&source); err != nil {
		return nil, false, fmt.Errorf("invalid request body for reasoning policy: %w", err)
	}
	topBudget, topSet, err := integerField(source, "thinking_token_budget")
	if err != nil {
		return nil, false, err
	}
	sourceNVExt, _ := source["nvext"].(map[string]any)
	nvBudget, nvSet, err := integerField(sourceNVExt, "max_thinking_tokens")
	if err != nil {
		return nil, false, err
	}
	if topSet && nvSet && topBudget != nvBudget {
		return nil, false, fmt.Errorf("thinking_token_budget and nvext.max_thinking_tokens conflict")
	}
	budget := policy.DefaultTokenBudget
	if topSet {
		budget = topBudget
	} else if nvSet {
		budget = nvBudget
	}
	if budget < 0 {
		return nil, false, fmt.Errorf("reasoning token budget must be non-negative")
	}
	if budget > policy.MaxTokenBudget {
		budget = policy.MaxTokenBudget
	}
	if output, set, fieldErr := completionLimit(source); fieldErr != nil {
		return nil, false, fieldErr
	} else if set {
		available := output - policy.AnswerReserveTokens
		if available < 0 {
			available = 0
		}
		if budget > available {
			budget = available
		}
	}
	targetDecoder := json.NewDecoder(bytes.NewReader(targetBody))
	targetDecoder.UseNumber()
	var request map[string]any
	if err := targetDecoder.Decode(&request); err != nil {
		return nil, false, fmt.Errorf("invalid translated request body for reasoning policy: %w", err)
	}
	delete(request, "thinking_token_budget")
	targetNVExt, _ := request["nvext"].(map[string]any)
	if targetNVExt == nil && sourceNVExt != nil {
		targetNVExt = make(map[string]any, len(sourceNVExt))
		for key, value := range sourceNVExt {
			targetNVExt[key] = value
		}
	}
	if targetNVExt != nil {
		delete(targetNVExt, "max_thinking_tokens")
		if len(targetNVExt) == 0 {
			delete(request, "nvext")
		}
	}
	if policy.Transport == "dynamo_nvext" {
		if targetNVExt == nil {
			targetNVExt = map[string]any{}
		}
		targetNVExt["max_thinking_tokens"] = budget
		request["nvext"] = targetNVExt
	} else {
		request["thinking_token_budget"] = budget
	}
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("encode reasoning request: %w", err)
	}
	return normalized, true, nil
}

func integerField(object map[string]any, key string) (int64, bool, error) {
	if object == nil {
		return 0, false, nil
	}
	value, ok := object[key]
	if !ok {
		return 0, false, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
	parsed, err := number.Int64()
	if err != nil {
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, true, nil
}

func completionLimit(request map[string]any) (int64, bool, error) {
	if value, set, err := integerField(request, "max_completion_tokens"); set || err != nil {
		return value, set, err
	}
	return integerField(request, "max_tokens")
}
