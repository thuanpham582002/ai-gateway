// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequestEventJSON(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 3, 23, 10, 30, 0, 0, time.UTC)
	event := &RequestEvent{
		EventType:         "request_completed",
		Timestamp:         ts,
		RequestID:         "req-123",
		Operation:         "chat",
		OriginalModel:     "qwen3-0.6b",
		RequestModel:      "qwen3-0.6b",
		ResponseModel:     "Qwen/Qwen3-0.6B",
		Backend:           "openai",
		BackendName:       "default/vllm-pool/route/my-route/rule/0/ref/0",
		Success:           true,
		LatencyMs:         320.5,
		Stream:            true,
		TimeToFirstTokenMs: 85.2,
		SelectedPool:      "vllm-pool-v2",
		ModelNameOverride: "Qwen/Qwen3-0.6B",
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
