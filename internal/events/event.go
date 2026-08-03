// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import "time"

// RequestEvent represents a single per-request event emitted to Kafka.
type RequestEvent struct {
	SchemaVersion       int               `json:"schema_version"`
	EventID             string            `json:"event_id"`
	RunID               string            `json:"run_id"`
	EventType           string            `json:"event_type"`
	Timestamp           time.Time         `json:"timestamp"`
	RequestID           string            `json:"request_id"`
	Operation           string            `json:"operation"`
	Protocol            string            `json:"protocol"`
	Transport           string            `json:"transport"`
	Client              *ClientInfo       `json:"client,omitempty"`
	ConversationKey     string            `json:"conversation_key,omitempty"`
	Correlation         *CorrelationInfo  `json:"correlation,omitempty"`
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
	Billing             *BillingInfo      `json:"billing,omitempty"`
	CapturePolicy       CapturePolicy     `json:"capture_policy"`
	Headers             map[string]string `json:"headers,omitempty"`
	SelectedPool        string            `json:"selected_pool,omitempty"`
	ModelNameOverride   string            `json:"model_name_override,omitempty"`
	RequestAudit        *RequestAudit     `json:"request_audit,omitempty"`
	ParseFailure        *ParseFailureInfo `json:"parse_failure,omitempty"`
	RequestBody         *BodySnapshot     `json:"request_body,omitempty"`
	UpstreamRequestBody *BodySnapshot     `json:"upstream_request_body,omitempty"`
	ResponseBody        *BodySnapshot     `json:"response_body,omitempty"`
}

// ParseFailureInfo is a bounded, content-free diagnostic for requests rejected
// before routing. It intentionally records only tool discriminators.
type ParseFailureInfo struct {
	Stage              string   `json:"stage"`
	Message            string   `json:"message"`
	ToolTypes          []string `json:"tool_types,omitempty"`
	ToolTypesTruncated bool     `json:"tool_types_truncated"`
}

// RequestAudit retains non-content request options and actual tool calls while
// excluding prompts, messages, inputs, tool results, and generated text.
type RequestAudit struct {
	Body          map[string]any  `json:"body,omitempty"`
	ToolCalls     []ToolCallAudit `json:"tool_calls,omitempty"`
	Truncated     bool            `json:"truncated"`
	argumentBytes int
}

// ToolCallAudit is the bounded, content-free routing view of a tool call.
// Arguments are retained explicitly for operational debugging.
type ToolCallAudit struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ClientInfo identifies a known client without making client detection part of routing.
type ClientInfo struct {
	Name string `json:"name"`
}

// BillingInfo projects processed billing metadata into a stable typed object.
type BillingInfo struct {
	APIKeyID             string  `json:"api_key_id,omitempty"`
	BillingRequestID     string  `json:"billing_request_id,omitempty"`
	TenantID             string  `json:"tenant_id,omitempty"`
	SubjectID            string  `json:"subject_id,omitempty"`
	SubjectType          string  `json:"subject_type,omitempty"`
	ProjectID            string  `json:"project_id,omitempty"`
	InputPrice           string  `json:"input_price,omitempty"`
	OutputPrice          string  `json:"output_price,omitempty"`
	ReservedCost         string  `json:"reserved_cost,omitempty"`
	CreditRemaining      string  `json:"credit_remaining,omitempty"`
	RateLimitRemaining   string  `json:"rate_limit_remaining,omitempty"`
	IsFree               *bool   `json:"is_free,omitempty"`
	AdmissionReserved    *bool   `json:"admission_reserved,omitempty"`
	ReservedInputTokens  *uint64 `json:"reserved_input_tokens,omitempty"`
	ReservedOutputTokens *uint64 `json:"reserved_output_tokens,omitempty"`
	ProjectInFlightSlot  *int64  `json:"project_in_flight_slot,omitempty"`
	APIKeyInFlightSlot   *int64  `json:"api_key_in_flight_slot,omitempty"`
	ModelConcurrencySlot *int64  `json:"model_concurrency_slot,omitempty"`
}

// CapturePolicy declares how request and response bodies were retained for this event.
type CapturePolicy struct {
	Mode              string               `json:"mode"`
	MaxInlineBytes    int                  `json:"max_inline_bytes"`
	ExternalBodyStore *ExternalStorePolicy `json:"external_body_store,omitempty"`
}

// ExternalStorePolicy describes the configured overflow store without exposing credentials.
type ExternalStorePolicy struct {
	Provider     string `json:"provider"`
	MaxBodyBytes int64  `json:"max_body_bytes"`
}

// CorrelationInfo contains native lineage identifiers when the wire protocol provides them.
type CorrelationInfo struct {
	SessionID          string `json:"session_id,omitempty"`
	ConversationID     string `json:"conversation_id,omitempty"`
	BranchID           string `json:"branch_id,omitempty"`
	ThreadID           string `json:"thread_id,omitempty"`
	ParentThreadID     string `json:"parent_thread_id,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
	WindowID           string `json:"window_id,omitempty"`
	ParentTurnID       string `json:"parent_turn_id,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	ResponseID         string `json:"response_id,omitempty"`
	ProviderResponseID string `json:"provider_response_id,omitempty"`
	ForkedFromThreadID string `json:"forked_from_thread_id,omitempty"`
	AgentID            string `json:"agent_id,omitempty"`
	ParentAgentID      string `json:"parent_agent_id,omitempty"`
}

// BodySnapshot fingerprints complete body bytes and optionally includes bounded content.
type BodySnapshot struct {
	SHA256               string               `json:"sha256"`
	SizeBytes            int64                `json:"size_bytes"`
	SameAs               string               `json:"same_as,omitempty"`
	Encoding             string               `json:"encoding,omitempty"`
	Content              string               `json:"content,omitempty"`
	Truncated            bool                 `json:"truncated"`
	Object               *BodyObjectReference `json:"object,omitempty"`
	ExternalStorageError string               `json:"external_storage_error,omitempty"`
}

// BodyObjectReference locates a complete body in an S3-compatible object store.
type BodyObjectReference struct {
	Provider  string `json:"provider"`
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	ETag      string `json:"etag,omitempty"`
	VersionID string `json:"version_id,omitempty"`
}

// TokenInfo holds token usage information for a request.
type TokenInfo struct {
	InputTokens              uint32 `json:"input_tokens"`
	OutputTokens             uint32 `json:"output_tokens"`
	TotalTokens              uint32 `json:"total_tokens"`
	CachedInputTokens        uint32 `json:"cached_input_tokens,omitempty"`
	CacheCreationInputTokens uint32 `json:"cache_creation_input_tokens,omitempty"`
}
