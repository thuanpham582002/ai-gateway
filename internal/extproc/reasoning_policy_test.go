package extproc

import (
	"encoding/json"
	"testing"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestReasoningPolicyForModel(t *testing.T) {
	policy := `{"enabled":true,"maxTokenBudget":32768,"transport":"dynamo_nvext"}`
	backend := func(model, value string) *filterapi.RuntimeBackend {
		return &filterapi.RuntimeBackend{Backend: &filterapi.Backend{
			ModelNameOverride: model,
			HeaderMutation: &filterapi.HTTPHeaderMutation{Set: []filterapi.HTTPHeader{
				{Name: reasoningPolicyHeader, Value: value},
			}},
		}}
	}
	config := &filterapi.RuntimeConfig{Backends: map[string]*filterapi.RuntimeBackend{
		"alias-route":  backend("glm-internal", policy),
		"direct-route": backend("glm-internal", policy),
		"other-model":  {Backend: &filterapi.Backend{ModelNameOverride: "qwen-internal"}},
	}}

	got, err := reasoningPolicyForModel(config, "glm-internal")
	if err != nil || got != policy {
		t.Fatalf("policy=%q err=%v", got, err)
	}
}

func TestReasoningPolicyForModelRejectsAmbiguousShapePolicy(t *testing.T) {
	backend := func(value string) *filterapi.RuntimeBackend {
		return &filterapi.RuntimeBackend{Backend: &filterapi.Backend{
			ModelNameOverride: "glm-internal",
			HeaderMutation: &filterapi.HTTPHeaderMutation{Set: []filterapi.HTTPHeader{
				{Name: reasoningPolicyHeader, Value: value},
			}},
		}}
	}
	config := &filterapi.RuntimeConfig{Backends: map[string]*filterapi.RuntimeBackend{
		"a": backend(`{"enabled":true,"maxTokenBudget":1,"transport":"dynamo_nvext"}`),
		"b": backend(`{"enabled":true,"maxTokenBudget":2,"transport":"dynamo_nvext"}`),
	}}

	if _, err := reasoningPolicyForModel(config, "glm-internal"); err == nil {
		t.Fatal("expected conflicting policies to be rejected")
	}
}

func TestNormalizeReasoningBudget(t *testing.T) {
	policy := `{"enabled":true,"defaultTokenBudget":32768,"maxTokenBudget":65536,"answerReserveTokens":4096,"transport":"dynamo_nvext"}`
	tests := []struct {
		name string
		body string
		want int64
	}{
		{name: "default", body: `{"model":"glm"}`, want: 32768},
		{name: "preserve client", body: `{"model":"glm","thinking_token_budget":16000}`, want: 16000},
		{name: "clamp shape max", body: `{"model":"glm","thinking_token_budget":100000}`, want: 65536},
		{name: "reserve answer", body: `{"model":"glm","thinking_token_budget":16000,"max_tokens":8192}`, want: 4096},
		{name: "explicit zero", body: `{"model":"glm","thinking_token_budget":0}`, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := normalizeReasoningBudget(policy, []byte(tt.body))
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			var decoded struct {
				NVExt struct {
					Budget int64 `json:"max_thinking_tokens"`
				} `json:"nvext"`
			}
			if err := json.Unmarshal(got, &decoded); err != nil || decoded.NVExt.Budget != tt.want {
				t.Fatalf("body=%s budget=%d err=%v", got, decoded.NVExt.Budget, err)
			}
		})
	}
}

func TestNormalizeReasoningBudgetRejectsConflictingClientFields(t *testing.T) {
	policy := `{"enabled":true,"defaultTokenBudget":10,"maxTokenBudget":20,"transport":"dynamo_nvext"}`
	_, _, err := normalizeReasoningBudget(policy, []byte(`{"thinking_token_budget":5,"nvext":{"max_thinking_tokens":6}}`))
	if err == nil {
		t.Fatal("expected conflict")
	}
}

func TestNormalizeReasoningBudgetAfterTranslationPreservesClientExtensions(t *testing.T) {
	policy := `{"enabled":true,"defaultTokenBudget":32768,"maxTokenBudget":32768,"transport":"dynamo_nvext"}`
	source := []byte(`{"model":"glm-5.2","thinking_token_budget":8192,"nvext":{"trace":"keep"}}`)
	target := []byte(`{"model":"GLM-5.2 FP8 HLA Dynamo 1.3.0"}`)

	got, changed, err := normalizeReasoningBudgetAfterTranslation(policy, source, target)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var decoded struct {
		Model string `json:"model"`
		NVExt struct {
			Budget int64  `json:"max_thinking_tokens"`
			Trace  string `json:"trace"`
		} `json:"nvext"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "GLM-5.2 FP8 HLA Dynamo 1.3.0" || decoded.NVExt.Budget != 8192 || decoded.NVExt.Trace != "keep" {
		t.Fatalf("unexpected translated body: %s", got)
	}
}
