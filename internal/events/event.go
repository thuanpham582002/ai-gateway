// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import "time"

// RequestEvent represents a single per-request event emitted to Kafka.
type RequestEvent struct {
	EventType           string            `json:"event_type"`
	Timestamp           time.Time         `json:"timestamp"`
	RequestID           string            `json:"request_id"`
	Operation           string            `json:"operation"`
	OriginalModel       string            `json:"original_model"`
	RequestModel        string            `json:"request_model"`
	ResponseModel       string            `json:"response_model"`
	Backend             string            `json:"backend"`
	BackendName         string            `json:"backend_name,omitempty"`
	Success             bool              `json:"success"`
	ErrorType           string            `json:"error_type,omitempty"`
	LatencyMs           float64           `json:"latency_ms"`
	Tokens              *TokenInfo        `json:"tokens,omitempty"`
	Stream              bool              `json:"stream"`
	TimeToFirstTokenMs  float64           `json:"time_to_first_token_ms,omitempty"`
	InterTokenLatencyMs float64           `json:"inter_token_latency_ms,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	SelectedPool        string            `json:"selected_pool,omitempty"`
	ModelNameOverride   string            `json:"model_name_override,omitempty"`
}

// TokenInfo holds token usage information for a request.
type TokenInfo struct {
	InputTokens              uint32 `json:"input_tokens"`
	OutputTokens             uint32 `json:"output_tokens"`
	TotalTokens              uint32 `json:"total_tokens"`
	CachedInputTokens        uint32 `json:"cached_input_tokens,omitempty"`
	CacheCreationInputTokens uint32 `json:"cache_creation_input_tokens,omitempty"`
}
