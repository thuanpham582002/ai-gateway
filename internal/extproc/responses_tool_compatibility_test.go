package extproc

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNormalizeResponsesCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		changed bool
	}{
		{
			name:    "missing format defaults to text",
			body:    `{"model":"model-canary","tools":[{"type":"custom","name":"shell"}]}`,
			changed: true,
		},
		{
			name:    "null format defaults to text",
			body:    `{"tools":[{"type":"custom","name":"shell","format":null}]}`,
			changed: true,
		},
		{
			name:    "existing grammar is preserved",
			body:    `{"tools":[{"type":"custom","name":"parser","format":{"type":"grammar","syntax":"lark","definition":"start: WORD"}}]}`,
			changed: false,
		},
		{
			name:    "other tools are unchanged",
			body:    `{"tools":[{"type":"function","name":"f"},{"type":"namespace","name":"apps"},{"type":"tool_search"},{"type":"web_search"}]}`,
			changed: false,
		},
		{
			name:    "text verbosity gets default format",
			body:    `{"text":{"verbosity":"high"}}`,
			changed: true,
		},
		{
			name:    "existing text format is preserved",
			body:    `{"text":{"verbosity":"high","format":{"type":"json_schema","name":"answer","schema":{}}}}`,
			changed: false,
		},
		{name: "no tools", body: `{"input":"hi"}`, changed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := normalizeResponsesCompatibility([]byte(tt.body))
			if err != nil || changed != tt.changed {
				t.Fatalf("changed=%v err=%v body=%s", changed, err, got)
			}
			if !changed {
				if string(got) != tt.body {
					t.Fatalf("unchanged request was rewritten: %s", got)
				}
				return
			}
			var request map[string]any
			if err := json.Unmarshal(got, &request); err != nil {
				t.Fatal(err)
			}
			var format map[string]any
			if tools, ok := request["tools"].([]any); ok {
				format = tools[0].(map[string]any)["format"].(map[string]any)
			} else {
				format = request["text"].(map[string]any)["format"].(map[string]any)
			}
			if format["type"] != "text" {
				t.Fatalf("format=%v body=%s", format, got)
			}
		})
	}
}

func TestNormalizeResponsesCompatibilityPreservesRequest(t *testing.T) {
	body := []byte(`{"model":"model-canary","instructions":"keep this","input":[{"role":"user","content":"hi"}],"text":{"verbosity":"high"},"tools":[{"type":"custom","name":"shell","description":"run"},{"type":"namespace","name":"apps","vendor":{"enabled":true}},{"type":"tool_search","execution":"client"}],"max_output_tokens":128,"vendor_limit":9007199254740993}`)
	got, changed, err := normalizeResponsesCompatibility(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(got))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request["instructions"] != "keep this" || request["vendor_limit"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("request fields were not preserved: %s", got)
	}
	text := request["text"].(map[string]any)
	if text["verbosity"] != "high" || text["format"].(map[string]any)["type"] != "text" {
		t.Fatalf("text defaults were not normalized: %s", got)
	}
	tools := request["tools"].([]any)
	if tools[1].(map[string]any)["type"] != "namespace" || tools[2].(map[string]any)["type"] != "tool_search" {
		t.Fatalf("extension tools were not preserved: %s", got)
	}
}
