// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import "context"

// Publisher accumulates per-request state and publishes a complete event at request completion.
// One instance is created per request, mirroring the lifecycle of metrics.Metrics.
type Publisher interface {
	SetRequestID(id string)
	SetOriginalModel(model string)
	SetRequestModel(model string)
	SetResponseModel(model string)
	SetBackend(backend string)
	SetBackendName(name string)
	SetSelectedPool(pool string)
	SetModelNameOverride(override string)
	SetStream(stream bool)
	SetRequestHeaders(headers map[string]string)
	// Publish emits the accumulated event. Called once at request completion.
	Publish(ctx context.Context, success bool, errorType string, tokens *TokenInfo, latencyMs, ttftMs, itlMs float64)
}

// Factory creates per-request Publisher instances.
type Factory interface {
	NewPublisher(operation string) Publisher
}
