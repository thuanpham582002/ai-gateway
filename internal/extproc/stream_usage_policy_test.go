package extproc

import (
	"encoding/json"
	"testing"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestStreamUsagePolicyForModel(t *testing.T) {
	policy := `{"enabled":true}`
	backend := func(model, value string) *filterapi.RuntimeBackend {
		return &filterapi.RuntimeBackend{Backend: &filterapi.Backend{
			ModelNameOverride: model,
			HeaderMutation: &filterapi.HTTPHeaderMutation{Set: []filterapi.HTTPHeader{{
				Name: streamUsagePolicyHeader, Value: value,
			}}},
		}}
	}
	config := &filterapi.RuntimeConfig{Backends: map[string]*filterapi.RuntimeBackend{
		"alias": backend("qwen", policy), "direct": backend("qwen", policy),
	}}
	got, err := streamUsagePolicyForModel(config, "qwen")
	if err != nil || got != policy {
		t.Fatalf("policy=%q err=%v", got, err)
	}
}

func TestStreamUsagePolicyForModelRejectsConflict(t *testing.T) {
	backend := func(value string) *filterapi.RuntimeBackend {
		return &filterapi.RuntimeBackend{Backend: &filterapi.Backend{
			ModelNameOverride: "qwen",
			HeaderMutation:    &filterapi.HTTPHeaderMutation{Set: []filterapi.HTTPHeader{{Name: streamUsagePolicyHeader, Value: value}}},
		}}
	}
	config := &filterapi.RuntimeConfig{Backends: map[string]*filterapi.RuntimeBackend{
		"a": backend(`{"enabled":true}`), "b": backend(`{"enabled":false}`),
	}}
	if _, err := streamUsagePolicyForModel(config, "qwen"); err == nil {
		t.Fatal("expected conflicting policies to be rejected")
	}
}

func TestNormalizeStreamUsage(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		body    string
		changed bool
		want    any
	}{
		{name: "enabled injects", policy: `{"enabled":true}`, body: `{"stream":true}`, changed: true, want: true},
		{name: "enabled overrides false", policy: `{"enabled":true}`, body: `{"stream":true,"stream_options":{"include_usage":false}}`, changed: true, want: true},
		{name: "enabled preserves true", policy: `{"enabled":true}`, body: `{"stream":true,"stream_options":{"include_usage":true}}`, changed: false, want: true},
		{name: "disabled preserves client", policy: `{"enabled":false}`, body: `{"stream":true,"stream_options":{"include_usage":false}}`, changed: false, want: false},
		{name: "non streaming unchanged", policy: `{"enabled":true}`, body: `{"stream":false}`, changed: false, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := normalizeStreamUsage(tt.policy, []byte(tt.body))
			if err != nil || changed != tt.changed {
				t.Fatalf("changed=%v err=%v body=%s", changed, err, got)
			}
			var decoded map[string]any
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatal(err)
			}
			var include any
			if options, ok := decoded["stream_options"].(map[string]any); ok {
				include = options["include_usage"]
			}
			if include != tt.want {
				t.Fatalf("include_usage=%v want=%v body=%s", include, tt.want, got)
			}
		})
	}
}
