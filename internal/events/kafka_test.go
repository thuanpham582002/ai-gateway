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
	"os"
	"path/filepath"
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
		producer:            mockProducer,
		topic:               "test-topic",
		headerKeys:          map[string]bool{"x-session-id": true, "authorization": true},
		bodyCaptureMaxBytes: 1024,
		logger:              logger,
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
	p.SetRequestBody([]byte(`{"model":"qwen3-0.6b","messages":[]}`))
	p.SetUpstreamRequestBody([]byte(`{"model":"Qwen/Qwen3-0.6B","messages":[]}`))
	p.ObserveResponseBody([]byte(`{"model":"Qwen/Qwen3-0.6B"}`))

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
	require.Equal(t, "openai.chat_completions", event.Protocol)
	require.Equal(t, "http_sse", event.Transport)
	require.NotEmpty(t, event.EventID)
	require.NotEmpty(t, event.RunID)
	require.NotEmpty(t, event.RequestBody.SHA256)
	require.False(t, event.RequestBody.Truncated)
	require.NotEmpty(t, event.UpstreamRequestBody.SHA256)
	require.NotEmpty(t, event.ResponseBody.SHA256)

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

func TestKafkaPublisherStoresBodyBeforePublishingReference(t *testing.T) {
	mockProducer := mocks.NewAsyncProducer(t, newTestConfig())
	mockProducer.ExpectInputAndSucceed()
	store := &recordingBodyStore{maxBytes: 1024}
	f := &kafkaFactory{
		producer: mockProducer, topic: "test-topic", headerKeys: map[string]bool{},
		bodyCaptureMaxBytes: 1, bodyStore: store,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	publisher := f.NewPublisher("chat")
	publisher.SetRequestID("req-s3")
	publisher.SetRequestBody([]byte("complete request"))
	publisher.Publish(t.Context(), true, "", nil, 1, 0, 0)

	message := <-mockProducer.Successes()
	value, err := message.Value.Encode()
	require.NoError(t, err)
	var event RequestEvent
	require.NoError(t, json.Unmarshal(value, &event))
	require.Len(t, store.objects, 1)
	require.Equal(t, "request.bin", event.RequestBody.Object.Key)
	require.Empty(t, event.RequestBody.ExternalStorageError)
}

type blockingBodyStore struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingBodyStore) MaxBodyBytes() int64 { return 1024 }

func (s *blockingBodyStore) Put(_ context.Context, object BodyObject) (*BodyObjectReference, error) {
	close(s.started)
	<-s.release
	return &BodyObjectReference{Provider: "s3", Bucket: "audit", Key: object.Kind + ".bin"}, nil
}

func TestKafkaPublisherSpoolsBeforeExternalUploadCompletes(t *testing.T) {
	mockProducer := mocks.NewAsyncProducer(t, newTestConfig())
	mockProducer.ExpectInputAndSucceed()
	store := &blockingBodyStore{started: make(chan struct{}), release: make(chan struct{})}
	spoolDir := t.TempDir()
	f := &kafkaFactory{
		producer: mockProducer, topic: "test-topic", headerKeys: map[string]bool{},
		bodyCaptureMaxBytes: 1, bodyStore: store, spoolDir: spoolDir,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	publisher := f.NewPublisher("chat")
	publisher.SetRequestID("req-pending")
	publisher.SetRequestBody([]byte("complete request"))
	publisher.Publish(t.Context(), true, "", nil, 1, 0, 0)
	<-store.started

	entries, err := os.ReadDir(spoolDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	spoolPath := filepath.Join(spoolDir, entries[0].Name())
	pendingMessage, err := readSpoolRecord(spoolPath)
	require.NoError(t, err)
	pendingValue, err := pendingMessage.Value.Encode()
	require.NoError(t, err)
	var pendingEvent RequestEvent
	require.NoError(t, json.Unmarshal(pendingValue, &pendingEvent))
	require.Equal(t, externalStorageUploadPending, pendingEvent.RequestBody.ExternalStorageError)
	require.Nil(t, pendingEvent.RequestBody.Object)

	close(store.release)
	message := <-mockProducer.Successes()
	value, err := message.Value.Encode()
	require.NoError(t, err)
	var finalEvent RequestEvent
	require.NoError(t, json.Unmarshal(value, &finalEvent))
	require.Empty(t, finalEvent.RequestBody.ExternalStorageError)
	require.Equal(t, "request.bin", finalEvent.RequestBody.Object.Key)
	require.Equal(t, spoolPath, message.Metadata)
}

func TestKafkaFactoryRejectsInvalidCustomCA(t *testing.T) {
	_, _, err := NewKafkaFactory(KafkaConfig{
		Brokers: []string{"kafka:9093"}, Topic: "events", TLSEnabled: true, TLSCAPEM: "invalid",
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.ErrorContains(t, err, "configure Kafka TLS")
	require.ErrorContains(t, err, "CA bundle contains no valid certificates")
}

// Ensure compile-time interface satisfaction.
var _ sarama.AsyncProducer = (*mocks.AsyncProducer)(nil)
