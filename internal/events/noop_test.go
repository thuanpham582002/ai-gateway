// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoopFactory(t *testing.T) {
	t.Parallel()
	f := NewNoopFactory()
	require.NotNil(t, f)

	pub := f.NewPublisher("chat")
	require.NotNil(t, pub)

	// All methods should be safe to call without panicking.
	pub.SetRequestID("req-123")
	pub.SetOriginalModel("model")
	pub.SetRequestModel("model")
	pub.SetResponseModel("model")
	pub.SetBackend("openai")
	pub.SetBackendName("backend")
	pub.SetSelectedPool("pool")
	pub.SetModelNameOverride("override")
	pub.SetStream(true)
	pub.SetRequestHeaders(map[string]string{"key": "value"})
	pub.Publish(context.Background(), true, "", &TokenInfo{InputTokens: 10}, 100.0, 50.0, 10.0)
}
