// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/require"
)

type fakeSyncProducer struct {
	mu       sync.Mutex
	messages []*sarama.ProducerMessage
	results  chan error
	notify   chan struct{}
	closed   atomic.Bool
}

func newFakeSyncProducer() *fakeSyncProducer {
	return &fakeSyncProducer{results: make(chan error, 16), notify: make(chan struct{}, 16)}
}

func (p *fakeSyncProducer) SendMessage(message *sarama.ProducerMessage) (int32, int64, error) {
	p.mu.Lock()
	p.messages = append(p.messages, message)
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
	select {
	case err := <-p.results:
		return 0, 0, err
	default:
		return 0, 0, nil
	}
}

func (p *fakeSyncProducer) Close() error { p.closed.Store(true); return nil }

func (p *fakeSyncProducer) waitMessage(t *testing.T) *sarama.ProducerMessage {
	t.Helper()
	select {
	case <-p.notify:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Kafka message")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.messages[len(p.messages)-1]
}

func newTestKafkaFactory(t *testing.T, producerFactory kafkaProducerFactory, mutate func(*KafkaConfig)) (*kafkaFactory, func()) {
	t.Helper()
	cfg := KafkaConfig{Topic: "test-topic", SpoolDir: t.TempDir(), OutboxMaxBytes: 1 << 20, OutboxMaxRecords: 100}
	if mutate != nil {
		mutate(&cfg)
	}
	factory, shutdown, err := newKafkaFactory(cfg, []string{"x-session-id", "authorization"}, slog.New(slog.NewTextHandler(io.Discard, nil)), producerFactory)
	require.NoError(t, err)
	return factory.(*kafkaFactory), shutdown
}

func TestKafkaPublisherDurablyPublishesEvent(t *testing.T) {
	producer := newFakeSyncProducer()
	factory, shutdown := newTestKafkaFactory(t, func() (kafkaSyncProducer, error) { return producer, nil }, nil)
	defer shutdown()
	publisher := factory.NewPublisher("chat")
	publisher.SetRequestID("req-001")
	publisher.SetOriginalModel("qwen3-0.6b")
	publisher.SetRequestModel("qwen3-0.6b")
	publisher.SetResponseModel("Qwen/Qwen3-0.6B")
	publisher.SetBackend("openai")
	publisher.SetStream(true)
	publisher.SetRequestHeaders(map[string]string{"x-session-id": "sess-abc", "authorization": "Bearer secret"})
	publisher.Publish(context.Background(), true, "", &TokenInfo{InputTokens: 100, OutputTokens: 200, TotalTokens: 300}, 250.5, 80, 12)

	message := producer.waitMessage(t)
	key, err := message.Key.Encode()
	require.NoError(t, err)
	require.Equal(t, "req-001", string(key))
	value, err := message.Value.Encode()
	require.NoError(t, err)
	var event RequestEvent
	require.NoError(t, json.Unmarshal(value, &event))
	require.Equal(t, "request_completed", event.EventType)
	require.Equal(t, "Qwen/Qwen3-0.6B", event.ResponseModel)
	require.Equal(t, uint32(300), event.Tokens.TotalTokens)
	require.Equal(t, "sess-abc", event.Headers["x-session-id"])
	require.NotContains(t, event.Headers, "authorization")
	require.Eventually(t, func() bool { records, _ := factory.outbox.stats(); return records == 0 }, 3*time.Second, 10*time.Millisecond)
}

func TestKafkaFactoryStartsAndPublishReturnsWhileKafkaDown(t *testing.T) {
	var attempts atomic.Int64
	factory, shutdown := newTestKafkaFactory(t, func() (kafkaSyncProducer, error) {
		attempts.Add(1)
		return nil, errors.New("broker unavailable")
	}, nil)
	defer shutdown()
	publisher := factory.NewPublisher("chat")
	publisher.SetRequestID("req-down")
	started := time.Now()
	publisher.Publish(context.Background(), true, "", nil, 1, 0, 0)
	require.Less(t, time.Since(started), 250*time.Millisecond)
	require.Eventually(t, func() bool { records, _ := factory.outbox.stats(); return records == 1 }, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return attempts.Load() > 0 }, time.Second, 10*time.Millisecond)
}

func TestKafkaShipperReconnectsAndAcknowledges(t *testing.T) {
	producer := newFakeSyncProducer()
	var attempts atomic.Int64
	factory, shutdown := newTestKafkaFactory(t, func() (kafkaSyncProducer, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("broker unavailable")
		}
		return producer, nil
	}, nil)
	defer shutdown()
	publisher := factory.NewPublisher("chat")
	publisher.SetRequestID("req-reconnect")
	publisher.Publish(context.Background(), true, "", nil, 1, 0, 0)
	require.Eventually(t, func() bool { return attempts.Load() >= 1 }, time.Second, 10*time.Millisecond)
	message := producer.waitMessage(t)
	key, err := message.Key.Encode()
	require.NoError(t, err)
	require.Equal(t, "req-reconnect", string(key))
	require.Eventually(t, func() bool { records, _ := factory.outbox.stats(); return records == 0 }, 3*time.Second, 10*time.Millisecond)
}

func TestKafkaShipperRetainsFailedSendUntilReconnect(t *testing.T) {
	failed := newFakeSyncProducer()
	failed.results <- errors.New("write failed")
	succeeded := newFakeSyncProducer()
	var attempts atomic.Int64
	factory, shutdown := newTestKafkaFactory(t, func() (kafkaSyncProducer, error) {
		if attempts.Add(1) == 1 {
			return failed, nil
		}
		return succeeded, nil
	}, nil)
	defer shutdown()
	publisher := factory.NewPublisher("chat")
	publisher.SetRequestID("req-retry")
	publisher.Publish(context.Background(), true, "", nil, 1, 0, 0)
	failed.waitMessage(t)
	records, _ := factory.outbox.stats()
	require.Equal(t, int64(1), records)
	succeeded.waitMessage(t)
	require.Eventually(t, func() bool { records, _ := factory.outbox.stats(); return records == 0 }, 3*time.Second, 10*time.Millisecond)
}

func TestKafkaPublisherQueueFullFailsOpenAndCountsDrop(t *testing.T) {
	factory, shutdown := newTestKafkaFactory(t, func() (kafkaSyncProducer, error) { return nil, errors.New("down") }, func(cfg *KafkaConfig) {
		cfg.OutboxMaxRecords = 1
	})
	defer shutdown()
	first := factory.NewPublisher("chat")
	first.SetRequestID("req-1")
	first.Publish(context.Background(), true, "", nil, 1, 0, 0)
	second := factory.NewPublisher("chat")
	second.SetRequestID("req-2")
	started := time.Now()
	second.Publish(context.Background(), true, "", nil, 1, 0, 0)
	require.Less(t, time.Since(started), 250*time.Millisecond)
	require.Equal(t, int64(1), factory.metrics.noopDropped.Load())
	records, _ := factory.outbox.stats()
	require.Equal(t, int64(1), records)
}

type blockingBodyStore struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingBodyStore) MaxBodyBytes() int64 { return 1024 }
func (s *blockingBodyStore) Put(ctx context.Context, object BodyObject) (*BodyObjectReference, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return &BodyObjectReference{Provider: "s3", Bucket: "audit", Key: object.Kind + ".bin"}, nil
	}
}

func TestKafkaPublisherHoldsPendingBodyUntilUploadCompletes(t *testing.T) {
	producer := newFakeSyncProducer()
	store := &blockingBodyStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	factory, shutdown := newTestKafkaFactory(t, func() (kafkaSyncProducer, error) { return producer, nil }, func(cfg *KafkaConfig) {
		cfg.BodyStore = store
		cfg.BodyCaptureMaxBytes = 1
	})
	defer shutdown()
	publisher := factory.NewPublisher("chat")
	publisher.SetRequestID("req-s3")
	publisher.SetRequestBody([]byte("complete request"))
	publisher.Publish(context.Background(), true, "", nil, 1, 0, 0)
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("upload did not start")
	}
	item, err := factory.outbox.peekReady()
	require.NoError(t, err)
	require.Nil(t, item)
	close(store.release)
	message := producer.waitMessage(t)
	value, err := message.Value.Encode()
	require.NoError(t, err)
	var event RequestEvent
	require.NoError(t, json.Unmarshal(value, &event))
	require.Equal(t, "request.bin", event.RequestBody.Object.Key)
	require.Empty(t, event.RequestBody.ExternalStorageError)
}

func TestKafkaFactoryRejectsInvalidCustomCA(t *testing.T) {
	_, _, err := NewKafkaFactory(KafkaConfig{Brokers: []string{"kafka:9093"}, Topic: "events", SpoolDir: t.TempDir(), TLSEnabled: true, TLSCAPEM: "invalid"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.ErrorContains(t, err, "configure Kafka TLS")
}
