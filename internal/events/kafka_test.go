// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/IBM/sarama"
	"github.com/IBM/sarama/mocks"
	"github.com/stretchr/testify/require"
)

func newTestConfig() *sarama.Config {
	cfg := sarama.NewConfig()
	cfg.Producer.Return.Successes = true
	return cfg
}

func TestKafkaPublisher_Publish(t *testing.T) {
	t.Parallel()
	mockProducer := mocks.NewAsyncProducer(t, newTestConfig())
	mockProducer.ExpectInputAndSucceed()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &kafkaFactory{
		producer:   mockProducer,
		topic:      "test-topic",
		headerKeys: map[string]bool{"x-session-id": true},
		logger:     logger,
	}

	pub := f.NewPublisher("chat")
	p := pub.(*kafkaPublisher)
	p.SetRequestID("req-001")
	p.SetOriginalModel("qwen3-0.6b")
	p.SetRequestModel("qwen3-0.6b")
	p.SetResponseModel("Qwen/Qwen3-0.6B")
	p.SetBackend("openai")
	p.SetBackendName("default/pool/route/r/rule/0/ref/0")
	p.SetSelectedPool("vllm-pool-v2")
	p.SetModelNameOverride("Qwen/Qwen3-0.6B")
	p.SetStream(true)
	p.SetRequestHeaders(map[string]string{
		"x-session-id":  "sess-abc",
		"authorization": "Bearer secret", // should be filtered out
	})

	tokens := &TokenInfo{InputTokens: 100, OutputTokens: 200, TotalTokens: 300}
	p.Publish(context.Background(), true, "", tokens, 250.5, 80.0, 12.0)

	// Read the message from the mock producer.
	msg := <-mockProducer.Successes()
	require.Equal(t, "test-topic", msg.Topic)

	key, err := msg.Key.Encode()
	require.NoError(t, err)
	require.Equal(t, "req-001", string(key))

	value, err := msg.Value.Encode()
	require.NoError(t, err)

	var event RequestEvent
	err = json.Unmarshal(value, &event)
	require.NoError(t, err)

	require.Equal(t, "request_completed", event.EventType)
	require.Equal(t, "req-001", event.RequestID)
	require.Equal(t, "chat", event.Operation)
	require.Equal(t, "qwen3-0.6b", event.OriginalModel)
	require.Equal(t, "Qwen/Qwen3-0.6B", event.ResponseModel)
	require.Equal(t, "openai", event.Backend)
	require.Equal(t, "vllm-pool-v2", event.SelectedPool)
	require.Equal(t, "Qwen/Qwen3-0.6B", event.ModelNameOverride)
	require.True(t, event.Success)
	require.Equal(t, 250.5, event.LatencyMs)
	require.Equal(t, 80.0, event.TimeToFirstTokenMs)
	require.Equal(t, 12.0, event.InterTokenLatencyMs)
	require.Equal(t, uint32(100), event.Tokens.InputTokens)
	require.Equal(t, uint32(200), event.Tokens.OutputTokens)

	// Only configured header keys should be included.
	require.Equal(t, "sess-abc", event.Headers["x-session-id"])
	require.NotContains(t, event.Headers, "authorization")
}

func TestKafkaPublisher_PublishFailure(t *testing.T) {
	t.Parallel()
	mockProducer := mocks.NewAsyncProducer(t, newTestConfig())
	mockProducer.ExpectInputAndSucceed()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &kafkaFactory{
		producer:   mockProducer,
		topic:      "test-topic",
		headerKeys: map[string]bool{},
		logger:     logger,
	}

	pub := f.NewPublisher("chat")
	pub.SetRequestID("req-002")
	pub.Publish(context.Background(), false, "backend_error", nil, 500.0, 0, 0)

	msg := <-mockProducer.Successes()
	value, err := msg.Value.Encode()
	require.NoError(t, err)

	var event RequestEvent
	err = json.Unmarshal(value, &event)
	require.NoError(t, err)

	require.Equal(t, "request_failed", event.EventType)
	require.False(t, event.Success)
	require.Equal(t, "backend_error", event.ErrorType)
	require.Nil(t, event.Tokens)
}

// Ensure compile-time interface satisfaction.
var _ sarama.AsyncProducer = (*mocks.AsyncProducer)(nil)
