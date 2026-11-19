// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type publisherState struct {
	runID                string
	requestID            string
	operation            string
	originalModel        string
	requestModel         string
	responseModel        string
	backend              string
	backendName          string
	selectedPool         string
	modelNameOverride    string
	stream               bool
	httpStatusCode       int
	backendErrorDetail   *BackendErrorDetail
	headers              map[string]string
	bodyCaptureMaxBytes  int
	bodyStore            BodyStore
	requestBody          *bodyCapture
	requestAudit         *RequestAudit
	messageAudit         *MessageAudit
	upstreamMessageAudit *MessageAudit
	parseFailure         *ParseFailureInfo
	upstreamRequestBody  *bodyCapture
	responseBody         *bodyCapture
	responseMetadata     []byte
	client               *ClientInfo
	correlation          *CorrelationInfo
}

func newPublisherState(operation string, bodyCaptureMaxBytes int) *publisherState {
	return newPublisherStateWithBodyStore(operation, bodyCaptureMaxBytes, nil)
}

func newPublisherStateWithBodyStore(operation string, bodyCaptureMaxBytes int, bodyStore BodyStore) *publisherState {
	return &publisherState{
		runID:               uuid.NewString(),
		operation:           operation,
		bodyCaptureMaxBytes: bodyCaptureMaxBytes,
		bodyStore:           bodyStore,
	}
}

func (p *publisherState) SetRequestID(id string)            { p.requestID = id }
func (p *publisherState) SetOriginalModel(model string)     { p.originalModel = model }
func (p *publisherState) SetRequestModel(model string)      { p.requestModel = model }
func (p *publisherState) SetResponseModel(model string)     { p.responseModel = model }
func (p *publisherState) SetBackend(backend string)         { p.backend = backend }
func (p *publisherState) SetBackendName(name string)        { p.backendName = name }
func (p *publisherState) SetSelectedPool(pool string)       { p.selectedPool = pool }
func (p *publisherState) SetModelNameOverride(value string) { p.modelNameOverride = value }
func (p *publisherState) SetStream(stream bool)             { p.stream = stream }
func (p *publisherState) SetHTTPStatusCode(code int)        { p.httpStatusCode = code }

const backendErrorFieldMaxBytes = 512

var backendErrorSecretPattern = regexp.MustCompile(`(?i)(bearer\s+|api[_-]?key[=: ]+|authorization[=: ]+|password[=: ]+|token[=: ]+)[^\s,;\"}]+`)

func (p *publisherState) SetBackendError(body []byte, headers map[string]string) {
	detail := parseBackendErrorDetail(body)
	for _, key := range []string{"x-request-id", "x-backend-request-id", "request-id", "x-amzn-requestid"} {
		if value := strings.TrimSpace(headers[key]); value != "" {
			detail.BackendRequestID, _ = boundedBackendErrorField(value)
			break
		}
	}
	if detail.Message == "" && detail.Type == "" && detail.Code == "" && detail.Param == "" && detail.BackendRequestID == "" {
		return
	}
	p.backendErrorDetail = detail
}

func parseBackendErrorDetail(body []byte) *BackendErrorDetail {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return &BackendErrorDetail{}
	}
	source := envelope
	if nested, ok := envelope["error"].(map[string]any); ok {
		source = nested
	}
	detail := &BackendErrorDetail{}
	for _, item := range []struct {
		keys   []string
		target *string
	}{
		{[]string{"message", "detail", "error"}, &detail.Message},
		{[]string{"type", "error_type"}, &detail.Type},
		{[]string{"code", "error_code"}, &detail.Code},
		{[]string{"param"}, &detail.Param},
	} {
		for _, key := range item.keys {
			value, ok := source[key]
			if !ok {
				value, ok = envelope[key]
			}
			if !ok {
				continue
			}
			text, ok := scalarErrorField(value)
			if !ok {
				continue
			}
			*item.target, ok = boundedBackendErrorField(text)
			detail.Truncated = detail.Truncated || ok
			break
		}
	}
	return detail
}

func scalarErrorField(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case float64, bool:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}

func boundedBackendErrorField(value string) (string, bool) {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	value = backendErrorSecretPattern.ReplaceAllString(value, "${1}[REDACTED]")
	if len(value) <= backendErrorFieldMaxBytes {
		return value, false
	}
	return value[:backendErrorFieldMaxBytes], true
}
func (p *publisherState) RunID() string { return p.runID }

func (p *publisherState) SetRequestHeaders(headers map[string]string) {
	p.headers = make(map[string]string, len(headers))
	for key, value := range headers {
		p.headers[strings.ToLower(key)] = value
	}
	p.extractNativeMetadata(nil)
}

func (p *publisherState) SetRequestBody(body []byte) {
	p.requestAudit = buildRequestAudit(body)
	p.messageAudit = buildMessageAudit(body)
	p.requestBody = newBodyCaptureWithExternalStore(p.bodyCaptureMaxBytes, p.externalBodyMaxBytes())
	p.requestBody.write(body)
	p.extractNativeMetadata(body)
}

func (p *publisherState) SetRequestParseFailure(body []byte, message string) {
	diagnostic, model, stream := buildParseFailureInfo(body, message)
	p.parseFailure = diagnostic
	p.originalModel = model
	p.requestModel = model
	p.stream = stream
	p.extractNativeMetadata(body)
}

func (p *publisherState) SetUpstreamRequestBody(body []byte) {
	p.upstreamMessageAudit = buildMessageAudit(body)
	p.upstreamRequestBody = newBodyCaptureWithExternalStore(p.bodyCaptureMaxBytes, p.externalBodyMaxBytes())
	p.upstreamRequestBody.write(body)
}

func (p *publisherState) ObserveResponseBody(body []byte) {
	if p.responseBody == nil {
		p.responseBody = newBodyCaptureWithExternalStore(p.bodyCaptureMaxBytes, p.externalBodyMaxBytes())
	}
	p.responseBody.write(body)
	p.observeNativeResponseMetadata(body)
}

func (p *publisherState) buildEvent(success bool, errorType string, tokens *TokenInfo, latencyMs, ttftMs, itlMs float64, headerKeys map[string]bool) *RequestEvent {
	eventType := "request_completed"
	if !success {
		eventType = "request_failed"
	}
	requestID := p.requestID
	if requestID == "" {
		requestID = "req-" + p.runID
	}
	requestBody := p.requestBody.snapshot()
	upstreamRequestBody := p.upstreamRequestBody.snapshot()
	if sameBody(requestBody, upstreamRequestBody) {
		upstreamRequestBody.SameAs = "request_body"
		upstreamRequestBody.Encoding = ""
		upstreamRequestBody.Content = ""
	}
	return &RequestEvent{
		SchemaVersion:        1,
		EventID:              uuid.NewString(),
		RunID:                p.runID,
		EventType:            eventType,
		Timestamp:            time.Now(),
		RequestID:            requestID,
		Operation:            p.operation,
		Protocol:             protocolFromOperation(p.operation),
		Transport:            transportForStream(p.stream),
		Client:               p.client,
		ConversationKey:      conversationKey(p.correlation),
		Correlation:          p.correlation,
		OriginalModel:        p.originalModel,
		RequestModel:         p.requestModel,
		ResponseModel:        p.responseModel,
		Backend:              p.backend,
		BackendName:          p.backendName,
		Success:              success,
		ErrorType:            errorType,
		HTTPStatusCode:       p.httpStatusCode,
		BackendErrorDetail:   p.backendErrorDetail,
		LatencyMs:            latencyMs,
		Tokens:               tokens,
		Stream:               p.stream,
		TimeToFirstTokenMs:   ttftMs,
		InterTokenLatencyMs:  itlMs,
		Billing:              billingInfo(p.headers),
		CapturePolicy:        capturePolicy(p.bodyCaptureMaxBytes, p.bodyStore),
		Headers:              filterEventHeaders(p.headers, headerKeys),
		SelectedPool:         p.selectedPool,
		ModelNameOverride:    p.modelNameOverride,
		RequestAudit:         p.requestAudit,
		MessageAudit:         p.messageAudit,
		UpstreamMessageAudit: p.upstreamMessageAudit,
		ParseFailure:         p.parseFailure,
		RequestBody:          requestBody,
		UpstreamRequestBody:  upstreamRequestBody,
		ResponseBody:         p.responseBody.snapshot(),
	}
}

func (p *publisherState) externalBodyMaxBytes() int64 {
	if p.bodyStore == nil {
		return 0
	}
	return p.bodyStore.MaxBodyBytes()
}

type bodyStoreFailure struct {
	kind string
	err  error
}

func (p *publisherState) storeExternalBodies(ctx context.Context, event *RequestEvent) []bodyStoreFailure {
	if p.bodyStore == nil {
		return nil
	}
	var failures []bodyStoreFailure
	for _, body := range []struct {
		kind     string
		capture  *bodyCapture
		snapshot *BodySnapshot
	}{
		{kind: "request", capture: p.requestBody, snapshot: event.RequestBody},
		{kind: "upstream-request", capture: p.upstreamRequestBody, snapshot: event.UpstreamRequestBody},
		{kind: "response", capture: p.responseBody, snapshot: event.ResponseBody},
	} {
		if body.snapshot == nil || !body.snapshot.Truncated || body.snapshot.SameAs != "" {
			continue
		}
		body.snapshot.ExternalStorageError = ""
		contents, ok := body.capture.completeBody()
		if !ok {
			body.snapshot.ExternalStorageError = externalStorageBodyTooLarge
			failures = append(failures, bodyStoreFailure{
				kind: body.kind,
				err:  fmt.Errorf("body exceeds external storage limit of %d bytes", p.bodyStore.MaxBodyBytes()),
			})
			continue
		}
		reference, err := p.bodyStore.Put(ctx, BodyObject{
			EventID:   event.EventID,
			RequestID: event.RequestID,
			Kind:      body.kind,
			Timestamp: event.Timestamp,
			SHA256:    body.snapshot.SHA256,
			Body:      contents,
		})
		if err != nil {
			body.snapshot.ExternalStorageError = externalStorageUploadFailed
			failures = append(failures, bodyStoreFailure{kind: body.kind, err: err})
			continue
		}
		body.snapshot.Object = reference
	}
	return failures
}

func markExternalBodiesPending(event *RequestEvent) {
	for _, snapshot := range []*BodySnapshot{event.RequestBody, event.UpstreamRequestBody, event.ResponseBody} {
		if snapshot != nil && snapshot.Truncated && snapshot.SameAs == "" {
			snapshot.ExternalStorageError = externalStorageUploadPending
		}
	}
}

func sameBody(first, second *BodySnapshot) bool {
	return first != nil && second != nil && first.SizeBytes == second.SizeBytes && first.SHA256 == second.SHA256
}

func protocolFromOperation(operation string) string {
	switch operation {
	case "chat":
		return "openai.chat_completions"
	case "completion":
		return "openai.completions"
	case "responses":
		return "openai.responses"
	case "responses_compact":
		return "openai.responses_compact"
	case "messages":
		return "anthropic.messages"
	default:
		return operation
	}
}

func transportForStream(stream bool) string {
	if stream {
		return "http_sse"
	}
	return "http"
}

func filterEventHeaders(headers map[string]string, headerKeys map[string]bool) map[string]string {
	if len(headers) == 0 || len(headerKeys) == 0 {
		return nil
	}
	filtered := make(map[string]string, len(headerKeys))
	for key := range headerKeys {
		if isSensitiveHeader(key) {
			continue
		}
		if value, ok := headers[key]; ok {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func isSensitiveHeader(key string) bool {
	key = strings.ReplaceAll(strings.TrimSpace(strings.ToLower(key)), "_", "-")
	switch key {
	case "authorization", "authentication", "cookie", "set-cookie", "api-key", "apikey":
		return true
	}
	for _, suffix := range []string{
		"-authorization", "-authentication", "-authenticate", "-cookie", "-api-key", "-apikey",
		"-auth", "-token", "-credential", "-password", "-secret", "-signature",
	} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}
