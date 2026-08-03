// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequestEventJSON(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 3, 23, 10, 30, 0, 0, time.UTC)
	event := &RequestEvent{
		EventType:          "request_completed",
		Timestamp:          ts,
		RequestID:          "req-123",
		Operation:          "chat",
		OriginalModel:      "qwen3-0.6b",
		RequestModel:       "qwen3-0.6b",
		ResponseModel:      "Qwen/Qwen3-0.6B",
		Backend:            "openai",
		BackendName:        "default/vllm-pool/route/my-route/rule/0/ref/0",
		Success:            true,
		LatencyMs:          320.5,
		Stream:             true,
		TimeToFirstTokenMs: 85.2,
		SelectedPool:       "vllm-pool-v2",
		ModelNameOverride:  "Qwen/Qwen3-0.6B",
		Tokens: &TokenInfo{
			InputTokens:  150,
			OutputTokens: 250,
			TotalTokens:  400,
		},
		Headers: map[string]string{"x-session-id": "sess-abc"},
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	var decoded RequestEvent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	require.Equal(t, event.EventType, decoded.EventType)
	require.Equal(t, event.RequestID, decoded.RequestID)
	require.Equal(t, event.Operation, decoded.Operation)
	require.Equal(t, event.OriginalModel, decoded.OriginalModel)
	require.Equal(t, event.Backend, decoded.Backend)
	require.Equal(t, event.SelectedPool, decoded.SelectedPool)
	require.Equal(t, event.ModelNameOverride, decoded.ModelNameOverride)
	require.True(t, decoded.Success)
	require.Equal(t, uint32(150), decoded.Tokens.InputTokens)
	require.Equal(t, uint32(250), decoded.Tokens.OutputTokens)
	require.Equal(t, uint32(400), decoded.Tokens.TotalTokens)
	require.Equal(t, 320.5, decoded.LatencyMs)
	require.Equal(t, "sess-abc", decoded.Headers["x-session-id"])
}

func TestRequestEventJSON_OmitEmpty(t *testing.T) {
	t.Parallel()
	event := &RequestEvent{
		EventType: "request_failed",
		Timestamp: time.Now(),
		RequestID: "req-456",
		Operation: "chat",
		Success:   false,
		ErrorType: "backend_error",
		LatencyMs: 100.0,
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	// Fields with omitempty should not appear when empty/zero.
	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	require.NotContains(t, raw, "tokens")
	require.NotContains(t, raw, "selected_pool")
	require.NotContains(t, raw, "model_name_override")
	require.NotContains(t, raw, "backend_name")
	require.NotContains(t, raw, "headers")
	require.Contains(t, raw, "error_type")
}

func TestPublisherStateDirectHTTP(t *testing.T) {
	t.Parallel()
	state := newPublisherState("chat", 1024)
	state.SetRequestID("req-curl")
	state.SetRequestHeaders(map[string]string{
		"user-agent":    "curl/8.7.1",
		"authorization": "Bearer secret",
	})
	request := []byte(`{"model":"demo","messages":[{"role":"user","content":"hello"}]}`)
	upstream := []byte(`{"model":"served","messages":[{"role":"user","content":"hello"}]}`)
	state.SetRequestBody(request)
	state.SetUpstreamRequestBody(upstream)
	state.ObserveResponseBody([]byte(`{"id":"resp-`))
	state.ObserveResponseBody([]byte(`1"}`))

	event := state.buildEvent(true, "", nil, 12, 0, 0, nil)
	require.Equal(t, 1, event.SchemaVersion)
	require.NotEmpty(t, event.EventID)
	require.NotEmpty(t, event.RunID)
	require.Equal(t, "req-curl", event.RequestID)
	require.Equal(t, "openai.chat_completions", event.Protocol)
	require.Equal(t, "http", event.Transport)
	require.Equal(t, CapturePolicy{Mode: "bounded_inline", MaxInlineBytes: 1024}, event.CapturePolicy)
	require.Equal(t, &ClientInfo{Name: "curl"}, event.Client)
	require.Equal(t, "resp-1", event.Correlation.ProviderResponseID)
	require.Nil(t, event.Headers, "headers are opt-in and must not leak authorization")
	require.Equal(t, string(request), event.RequestBody.Content)
	require.Equal(t, string(upstream), event.UpstreamRequestBody.Content)
	require.Equal(t, `{"id":"resp-1"}`, event.ResponseBody.Content)
	require.Equal(t, bodyHash(request), event.RequestBody.SHA256)
}

func TestPublisherStateBuildsSanitizedRequestAudit(t *testing.T) {
	t.Parallel()
	state := newPublisherState("chat", 1024)
	state.SetRequestBody([]byte(`{
		"model":"demo","stream":true,"max_tokens":128,
		"messages":[
			{"role":"user","content":"private prompt"},
			{"role":"assistant","content":"private answer","tool_calls":[
				{"id":"call-1","type":"function","function":{"name":"read","arguments":"{\"path\":\"/tmp/a\"}"}}
			]},
			{"role":"tool","tool_call_id":"call-1","content":"private tool result"}
		],
		"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]
	}`))

	event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
	require.NotNil(t, event.RequestAudit)
	require.False(t, event.RequestAudit.Truncated)
	require.NotContains(t, event.RequestAudit.Body, "messages")
	require.Equal(t, "demo", event.RequestAudit.Body["model"])
	require.Equal(t, float64(128), event.RequestAudit.Body["max_tokens"])
	require.Len(t, event.RequestAudit.ToolCalls, 1)
	require.Equal(t, ToolCallAudit{
		ID: "call-1", Type: "function", Name: "read", Arguments: `{"path":"/tmp/a"}`,
	}, event.RequestAudit.ToolCalls[0])

	encoded, err := json.Marshal(event.RequestAudit)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private prompt")
	require.NotContains(t, string(encoded), "private answer")
	require.NotContains(t, string(encoded), "private tool result")
}

func TestPublisherStateBuildsContentFreeParseFailureDiagnostic(t *testing.T) {
	t.Parallel()
	state := newPublisherState("responses", 4096)
	state.SetRequestHeaders(map[string]string{
		"user-agent":   "Codex Desktop/0.146.0-alpha.9.2",
		"x-request-id": "req-parse-1",
	})
	state.SetRequestID("req-parse-1")
	state.SetRequestParseFailure([]byte(`{
		"model":"model-canary",
		"stream":true,
		"instructions":"private system prompt",
		"input":"private user prompt",
		"tools":[
			{"type":"function","name":"private_function","parameters":{"secret":"value"}},
			{"type":"namespace","name":"private_namespace"},
			{"type":"tool_search","description":"private description"}
		]
	}`), "malformed request: unknown tool type")

	event := state.buildEvent(false, "malformed_request", nil, 0, 0, 0, nil)
	require.Equal(t, "model-canary", event.OriginalModel)
	require.Equal(t, "model-canary", event.RequestModel)
	require.True(t, event.Stream)
	require.Equal(t, &ParseFailureInfo{
		Stage: "request_body_parse", Message: "malformed request: unknown tool type",
		ToolTypes: []string{"function", "namespace", "tool_search"},
	}, event.ParseFailure)
	require.Nil(t, event.RequestAudit)
	require.Nil(t, event.RequestBody)

	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private system prompt")
	require.NotContains(t, string(encoded), "private user prompt")
	require.NotContains(t, string(encoded), "private_function")
	require.NotContains(t, string(encoded), "private_namespace")
	require.NotContains(t, string(encoded), "private description")
	require.NotContains(t, string(encoded), "secret")
}

func TestPublisherStateExtractsResponsesAndAnthropicToolArguments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation string
		body      string
		want      ToolCallAudit
	}{
		{
			name: "responses function call", operation: "responses",
			body: `{"model":"demo","input":[{"type":"message","content":"private"},{"type":"function_call","call_id":"call-r","name":"lookup","arguments":"{\"id\":7}"}]}`,
			want: ToolCallAudit{ID: "call-r", Type: "function_call", Name: "lookup", Arguments: `{"id":7}`},
		},
		{
			name: "anthropic tool use", operation: "messages",
			body: `{"model":"demo","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool-a","name":"shell","input":{"command":"date"}}]}]}`,
			want: ToolCallAudit{ID: "tool-a", Type: "tool_use", Name: "shell", Arguments: `{"command":"date"}`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newPublisherState(test.operation, 1024)
			state.SetRequestBody([]byte(test.body))
			event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
			require.NotNil(t, event.RequestAudit)
			require.Equal(t, []ToolCallAudit{test.want}, event.RequestAudit.ToolCalls)
		})
	}
}

func TestPublisherStateNeverEmitsCredentialHeaders(t *testing.T) {
	t.Parallel()
	state := newPublisherState("chat", 0)
	state.SetRequestHeaders(map[string]string{
		"authorization":                 "Bearer secret",
		"proxy-authorization":           "Basic secret",
		"authentication":                "secret",
		"x-auth":                        "secret",
		"cookie":                        "session=secret",
		"x-api-key":                     "secret",
		"x-goog-api-key":                "secret",
		"api_key":                       "secret",
		"x-access-token":                "secret",
		"x-client-secret":               "secret",
		"x-project-id":                  "project-123",
		"x-api-key-id":                  "key-id-123",
		"x-maas-api-key-in-flight-slot": "1",
		"x-maas-reserved-input-tokens":  "100",
	})

	event := state.buildEvent(true, "", nil, 0, 0, 0, map[string]bool{
		"authorization":                 true,
		"proxy-authorization":           true,
		"authentication":                true,
		"x-auth":                        true,
		"cookie":                        true,
		"x-api-key":                     true,
		"x-goog-api-key":                true,
		"api_key":                       true,
		"x-access-token":                true,
		"x-client-secret":               true,
		"x-project-id":                  true,
		"x-api-key-id":                  true,
		"x-maas-api-key-in-flight-slot": true,
		"x-maas-reserved-input-tokens":  true,
	})
	require.Equal(t, map[string]string{
		"x-project-id":                  "project-123",
		"x-api-key-id":                  "key-id-123",
		"x-maas-api-key-in-flight-slot": "1",
		"x-maas-reserved-input-tokens":  "100",
	}, event.Headers)
}

func TestPublisherStateProjectsTypedBillingMetadata(t *testing.T) {
	t.Parallel()
	state := newPublisherState("chat", 0)
	state.SetRequestHeaders(map[string]string{
		"x-api-key-id":                  "key-123",
		"x-maas-billing-request-id":     "billing-456",
		"x-tenant-id":                   "tenant-1",
		"x-subject-id":                  "subject-2",
		"x-subject-type":                "user",
		"x-project-id":                  "project-3",
		"x-input-price":                 "0.0001",
		"x-output-price":                "0.0002",
		"x-maas-reserved-cost":          "0.003",
		"x-credit-remaining":            "10.50",
		"x-rate-limit-remaining":        "99",
		"x-is-free":                     "false",
		"x-maas-admission-reserved":     "true",
		"x-maas-reserved-input-tokens":  "100",
		"x-maas-reserved-output-tokens": "25",
		"x-maas-project-in-flight-slot": "2",
		"x-maas-api-key-in-flight-slot": "3",
		"x-maas-model-concurrency-slot": "4",
	})

	event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
	require.Equal(t, CapturePolicy{Mode: "hash_only", MaxInlineBytes: 0}, event.CapturePolicy)
	require.NotNil(t, event.Billing)
	require.Equal(t, "key-123", event.Billing.APIKeyID)
	require.Equal(t, "billing-456", event.Billing.BillingRequestID)
	require.Equal(t, "tenant-1", event.Billing.TenantID)
	require.Equal(t, "subject-2", event.Billing.SubjectID)
	require.Equal(t, "user", event.Billing.SubjectType)
	require.Equal(t, "project-3", event.Billing.ProjectID)
	require.Equal(t, "0.0001", event.Billing.InputPrice)
	require.Equal(t, "0.0002", event.Billing.OutputPrice)
	require.Equal(t, "0.003", event.Billing.ReservedCost)
	require.Equal(t, "10.50", event.Billing.CreditRemaining)
	require.Equal(t, "99", event.Billing.RateLimitRemaining)
	require.Equal(t, false, *event.Billing.IsFree)
	require.Equal(t, true, *event.Billing.AdmissionReserved)
	require.Equal(t, uint64(100), *event.Billing.ReservedInputTokens)
	require.Equal(t, uint64(25), *event.Billing.ReservedOutputTokens)
	require.Equal(t, int64(2), *event.Billing.ProjectInFlightSlot)
	require.Equal(t, int64(3), *event.Billing.APIKeyInFlightSlot)
	require.Equal(t, int64(4), *event.Billing.ModelConcurrencySlot)
}

func TestPublisherStateOmitsInvalidTypedBillingValues(t *testing.T) {
	t.Parallel()
	state := newPublisherState("chat", 0)
	state.SetRequestHeaders(map[string]string{
		"x-is-free":                     "not-a-bool",
		"x-maas-reserved-input-tokens":  "-1",
		"x-maas-project-in-flight-slot": "not-an-int",
	})

	event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
	require.Nil(t, event.Billing)
}

func TestPublisherStateNativeCorrelation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		operation       string
		headers         map[string]string
		body            string
		client          string
		correlation     CorrelationInfo
		conversationKey string
	}{
		{
			name:      "codex responses metadata",
			operation: "responses",
			body: `{
				"model":"gpt-5-codex",
				"previous_response_id":"resp-parent",
				"client_metadata":{"x-codex-turn-metadata":{
					"session_id":"session-1","thread_id":"thread-2","parent_thread_id":"thread-parent","turn_id":"turn-3",
					"window_id":"window-4","forked_from_thread_id":"thread-1"
				}}
			}`,
			client: "codex",
			correlation: CorrelationInfo{
				SessionID:          "session-1",
				ThreadID:           "thread-2",
				ParentThreadID:     "thread-parent",
				TurnID:             "turn-3",
				WindowID:           "window-4",
				ForkedFromThreadID: "thread-1",
				PreviousResponseID: "resp-parent",
			},
			conversationKey: "session:session-1/thread:thread-2",
		},
		{
			name:      "claude code headers",
			operation: "messages",
			headers: map[string]string{
				"x-claude-code-session-id":      "session-1",
				"x-claude-code-agent-id":        "agent-2",
				"x-claude-code-parent-agent-id": "agent-1",
			},
			body:   `{"model":"claude-sonnet","messages":[{"role":"user","content":"hello"}]}`,
			client: "claude_code",
			correlation: CorrelationInfo{
				SessionID:     "session-1",
				AgentID:       "agent-2",
				ParentAgentID: "agent-1",
			},
			conversationKey: "session:session-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := newPublisherState(test.operation, 4096)
			state.SetRequestHeaders(test.headers)
			state.SetRequestBody([]byte(test.body))
			event := state.buildEvent(true, "", nil, 1, 0, 0, nil)
			require.Equal(t, test.client, event.Client.Name)
			require.Equal(t, test.correlation, *event.Correlation)
			require.Equal(t, test.conversationKey, event.ConversationKey)
		})
	}
}

func TestPublisherStateExtractsProviderResponseID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		operation       string
		chunks          [][]byte
		want            string
		wantResponsesID bool
	}{
		{
			name:            "Responses JSON",
			operation:       "responses",
			chunks:          [][]byte{[]byte(`{"id":"resp-json","object":"response"}`)},
			want:            "resp-json",
			wantResponsesID: true,
		},
		{
			name:      "Responses split SSE",
			operation: "responses",
			chunks: [][]byte{
				[]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-"),
				[]byte("sse\"}}\n\n"),
			},
			want:            "resp-sse",
			wantResponsesID: true,
		},
		{
			name:      "Chat Completions JSON",
			operation: "chat",
			chunks:    [][]byte{[]byte(`{"id":"chat-json","object":"chat.completion"}`)},
			want:      "chat-json",
		},
		{
			name:      "Chat Completions split SSE",
			operation: "chat",
			chunks: [][]byte{
				[]byte("data: {\"id\":\"chat-"),
				[]byte("sse\",\"object\":\"chat.completion.chunk\"}\n\n"),
			},
			want: "chat-sse",
		},
		{
			name:      "Messages JSON",
			operation: "messages",
			chunks:    [][]byte{[]byte(`{"id":"msg-json","type":"message"}`)},
			want:      "msg-json",
		},
		{
			name:      "Messages split SSE",
			operation: "messages",
			chunks: [][]byte{
				[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-"),
				[]byte("sse\"}}\n\n"),
			},
			want: "msg-sse",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := newPublisherState(test.operation, 0)
			for _, chunk := range test.chunks {
				state.ObserveResponseBody(chunk)
			}
			event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
			require.Equal(t, test.want, event.Correlation.ProviderResponseID)
			if test.wantResponsesID {
				require.Equal(t, test.want, event.Correlation.ResponseID)
			} else {
				require.Empty(t, event.Correlation.ResponseID)
			}
			require.Empty(t, event.ResponseBody.Content, "native metadata extraction must not enable inline capture")
		})
	}
}

func TestPublisherStateBoundsNativeResponseMetadata(t *testing.T) {
	t.Parallel()
	state := newPublisherState("responses", 0)
	state.ObserveResponseBody(bytes.Repeat([]byte("x"), nativeResponseMetadataMaxBytes+1))
	require.Len(t, state.responseMetadata, nativeResponseMetadataMaxBytes)

	state.SetRequestBody([]byte(`{"previous_response_id":"` + strings.Repeat("x", nativeIdentifierMaxBytes+1) + `"}`))
	event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
	require.Nil(t, event.Correlation)
}

func TestBodySnapshotIsBoundedButHashesFullBody(t *testing.T) {
	t.Parallel()
	body := []byte("0123456789")
	capture := newBodyCapture(4)
	capture.write(body[:5])
	capture.write(body[5:])
	snapshot := capture.snapshot()

	require.Equal(t, int64(len(body)), snapshot.SizeBytes)
	require.Equal(t, "0123", snapshot.Content)
	require.Equal(t, "utf-8", snapshot.Encoding)
	require.True(t, snapshot.Truncated)
	require.Equal(t, bodyHash(body), snapshot.SHA256)
}

func TestPublisherStateRecognizesDefaultClientUserAgents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		userAgent string
		operation string
		client    string
		protocol  string
	}{
		{userAgent: "opencode/1.2.3", operation: "chat", client: "opencode", protocol: "openai.chat_completions"},
		{userAgent: "opencode/1.2.3", operation: "responses", client: "opencode", protocol: "openai.responses"},
		{userAgent: "opencode/1.2.3", operation: "messages", client: "opencode", protocol: "anthropic.messages"},
		{userAgent: "codex-cli/1.0", operation: "responses", client: "codex", protocol: "openai.responses"},
		{userAgent: "claude-code/2", operation: "messages", client: "claude_code", protocol: "anthropic.messages"},
		{userAgent: "curl/8.7.1", operation: "chat", client: "curl", protocol: "openai.chat_completions"},
	}
	for _, test := range tests {
		state := newPublisherState(test.operation, 0)
		state.SetRequestHeaders(map[string]string{"user-agent": test.userAgent})
		event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
		require.Equal(t, test.client, event.Client.Name)
		require.Equal(t, test.protocol, event.Protocol)
	}
}

func TestPublisherStateGeneratesRequestID(t *testing.T) {
	t.Parallel()
	state := newPublisherState("messages", 0)
	event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
	require.Equal(t, "req-"+event.RunID, event.RequestID)
}

func TestPublisherStateDeduplicatesUnchangedUpstreamBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"demo","messages":[]}`)
	state := newPublisherState("chat", 1024)
	state.SetRequestBody(body)
	state.SetUpstreamRequestBody(body)

	event := state.buildEvent(true, "", nil, 0, 0, 0, nil)
	require.Equal(t, string(body), event.RequestBody.Content)
	require.Equal(t, "request_body", event.UpstreamRequestBody.SameAs)
	require.Empty(t, event.UpstreamRequestBody.Content)
	require.Equal(t, event.RequestBody.SHA256, event.UpstreamRequestBody.SHA256)
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum)
}
