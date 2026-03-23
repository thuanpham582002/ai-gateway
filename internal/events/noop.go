// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import "context"

// noopFactory returns noopPublisher instances with zero overhead.
type noopFactory struct{}

// NewNoopFactory creates a Factory that produces no-op publishers.
// Used when Kafka is not configured.
func NewNoopFactory() Factory { return &noopFactory{} }

func (f *noopFactory) NewPublisher(string) Publisher { return noopPublisher{} }

type noopPublisher struct{}

func (noopPublisher) SetRequestID(string)                {}
func (noopPublisher) SetOriginalModel(string)            {}
func (noopPublisher) SetRequestModel(string)             {}
func (noopPublisher) SetResponseModel(string)            {}
func (noopPublisher) SetBackend(string)                  {}
func (noopPublisher) SetBackendName(string)              {}
func (noopPublisher) SetSelectedPool(string)             {}
func (noopPublisher) SetModelNameOverride(string)        {}
func (noopPublisher) SetStream(bool)                     {}
func (noopPublisher) SetRequestHeaders(map[string]string) {}

func (noopPublisher) Publish(context.Context, bool, string, *TokenInfo, float64, float64, float64) {
}
