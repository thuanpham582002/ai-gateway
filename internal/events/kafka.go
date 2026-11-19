// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel/metric"
)

const (
	defaultOutboxMaxBytes   = int64(1 << 30)
	defaultOutboxMaxRecords = int64(1_000_000)
	defaultRetryMin         = 500 * time.Millisecond
	defaultRetryMax         = 30 * time.Second
)

// KafkaConfig holds Kafka producer and durable outbox configuration.
type KafkaConfig struct {
	Brokers             []string
	Topic               string
	SASLUser            string
	SASLPassword        string
	SASLMechanism       string
	TLSEnabled          bool
	TLSCABundle         string
	TLSCAPEM            string
	BodyCaptureMaxBytes int
	SpoolDir            string
	OutboxMaxBytes      int64
	OutboxMaxRecords    int64
	BodyStore           BodyStore
	Meter               metric.Meter
}

type kafkaSyncProducer interface {
	SendMessage(*sarama.ProducerMessage) (int32, int64, error)
	Close() error
}

type kafkaProducerFactory func() (kafkaSyncProducer, error)

type kafkaFactory struct {
	topic               string
	headerKeys          map[string]bool
	bodyCaptureMaxBytes int
	bodyStore           BodyStore
	logger              *slog.Logger
	outbox              *durableOutbox
	metrics             *outboxMetrics
	newProducer         kafkaProducerFactory
	wake                chan struct{}
	ctx                 context.Context
	cancel              context.CancelFunc
	shipperWG           sync.WaitGroup
	publishWG           sync.WaitGroup
}

// NewKafkaFactory opens the durable outbox and starts a reconnecting shipper.
// Kafka connectivity is deliberately not required for gateway startup.
func NewKafkaFactory(cfg KafkaConfig, headerKeys []string, logger *slog.Logger) (Factory, func(), error) {
	producerConfig, err := newSaramaProducerConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return newKafkaFactory(cfg, headerKeys, logger, func() (kafkaSyncProducer, error) {
		return sarama.NewSyncProducer(cfg.Brokers, producerConfig)
	})
}

func newKafkaFactory(cfg KafkaConfig, headerKeys []string, logger *slog.Logger, producerFactory kafkaProducerFactory) (Factory, func(), error) {
	if cfg.OutboxMaxBytes == 0 {
		cfg.OutboxMaxBytes = defaultOutboxMaxBytes
	}
	if cfg.OutboxMaxRecords == 0 {
		cfg.OutboxMaxRecords = defaultOutboxMaxRecords
	}
	outbox, err := openDurableOutbox(cfg.SpoolDir, cfg.OutboxMaxBytes, cfg.OutboxMaxRecords)
	if err != nil {
		return nil, nil, err
	}
	hk := make(map[string]bool, len(headerKeys))
	for _, key := range headerKeys {
		if key = strings.TrimSpace(strings.ToLower(key)); key != "" {
			hk[key] = true
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &kafkaFactory{
		topic: cfg.Topic, headerKeys: hk, bodyCaptureMaxBytes: cfg.BodyCaptureMaxBytes,
		bodyStore: cfg.BodyStore, logger: logger, outbox: outbox,
		metrics: newOutboxMetrics(cfg.Meter, outbox), newProducer: producerFactory,
		wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
	}
	f.shipperWG.Add(1)
	go f.runShipper()
	f.signalShipper()
	shutdown := func() {
		f.cancel()
		f.publishWG.Wait()
		f.shipperWG.Wait()
		if err := f.outbox.close(); err != nil {
			f.logger.Error("failed to close kafka event outbox", slog.Any("error", err))
		}
	}
	return f, shutdown, nil
}

func newSaramaProducerConfig(cfg KafkaConfig) (*sarama.Config, error) {
	saramaCfg := sarama.NewConfig()
	saramaCfg.Producer.Return.Errors = true
	saramaCfg.Producer.Return.Successes = true
	saramaCfg.Producer.Compression = sarama.CompressionSnappy
	saramaCfg.Producer.RequiredAcks = sarama.WaitForAll
	saramaCfg.Producer.Idempotent = true
	saramaCfg.Producer.Retry.Max = 3
	saramaCfg.Net.MaxOpenRequests = 1
	saramaCfg.Net.DialTimeout = 10 * time.Second
	saramaCfg.Net.ReadTimeout = 10 * time.Second
	saramaCfg.Net.WriteTimeout = 10 * time.Second
	if cfg.TLSEnabled {
		tlsConfig, err := tlsConfigWithAdditionalCA(cfg.TLSCABundle, cfg.TLSCAPEM)
		if err != nil {
			return nil, fmt.Errorf("configure Kafka TLS: %w", err)
		}
		saramaCfg.Net.TLS.Enable = true
		saramaCfg.Net.TLS.Config = tlsConfig
	}
	configureSASL(saramaCfg, cfg)
	return saramaCfg, nil
}

func configureSASL(config *sarama.Config, cfg KafkaConfig) {
	if cfg.SASLUser == "" {
		return
	}
	config.Net.SASL.Enable = true
	config.Net.SASL.User = cfg.SASLUser
	config.Net.SASL.Password = cfg.SASLPassword
	switch cfg.SASLMechanism {
	case "SCRAM-SHA-256":
		config.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA256
		config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA256} }
	case "SCRAM-SHA-512":
		config.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA512
		config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA512} }
	default:
		config.Net.SASL.Mechanism = sarama.SASLTypePlaintext
	}
}

func (f *kafkaFactory) NewPublisher(operation string) Publisher {
	return &kafkaPublisher{factory: f, publisherState: newPublisherStateWithBodyStore(operation, f.bodyCaptureMaxBytes, f.bodyStore)}
}

type kafkaPublisher struct {
	factory *kafkaFactory
	*publisherState
}

// Publish durably enqueues the event. Kafka network I/O never runs on the request path.
func (p *kafkaPublisher) Publish(_ context.Context, success bool, errorType string, tokens *TokenInfo, latencyMs, ttftMs, itlMs float64) {
	event := p.buildEvent(success, errorType, tokens, latencyMs, ttftMs, itlMs, p.factory.headerKeys)
	data, err := json.Marshal(event)
	if err != nil {
		p.factory.recordAppendFailure("marshal", err, event.EventID)
		return
	}
	ready := p.factory.bodyStore == nil
	if !ready {
		markExternalBodiesPending(event)
		data, err = json.Marshal(event)
		if err != nil {
			p.factory.recordAppendFailure("marshal_pending", err, event.EventID)
			return
		}
	}
	sequence, err := p.factory.outbox.enqueue(event.EventID, event.RequestID, data, ready)
	if err != nil {
		p.factory.recordAppendFailure("enqueue", err, event.EventID)
		return
	}
	p.factory.metrics.recordEnqueued()
	if ready {
		p.factory.signalShipper()
		return
	}
	p.factory.publishWG.Add(1)
	go func() {
		defer p.factory.publishWG.Done()
		for _, failure := range p.storeExternalBodies(p.factory.ctx, event) {
			p.factory.logger.Error("failed to store complete event body", slog.Any("error", failure.err), slog.String("body_kind", failure.kind), slog.String("event_id", event.EventID))
		}
		finalData, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			p.factory.recordAppendFailure("marshal_uploaded", marshalErr, event.EventID)
			return
		}
		if updateErr := p.factory.outbox.update(sequence, finalData, true); updateErr != nil {
			p.factory.recordAppendFailure("update_uploaded", updateErr, event.EventID)
			if readyErr := p.factory.outbox.markReady(sequence); readyErr != nil {
				p.factory.recordAppendFailure("mark_pending_ready", readyErr, event.EventID)
				return
			}
		}
		p.factory.signalShipper()
	}()
}

func (f *kafkaFactory) runShipper() {
	defer f.shipperWG.Done()
	var producer kafkaSyncProducer
	defer func() {
		if producer != nil {
			_ = producer.Close()
		}
	}()
	backoff := defaultRetryMin
	for {
		item, err := f.outbox.peekReady()
		var corrupt *corruptOutboxRecordError
		if errors.As(err, &corrupt) {
			f.logger.Error("quarantining corrupt kafka event outbox record", slog.Any("error", err))
			f.metrics.recordReplayFailure()
			if quarantineErr := f.outbox.quarantine(corrupt.sequence); quarantineErr != nil {
				f.logger.Error("failed to quarantine corrupt kafka event outbox record", slog.Any("error", quarantineErr))
				if !f.waitRetry(backoff) {
					return
				}
				backoff = nextBackoff(backoff)
			}
			continue
		}
		if err != nil {
			f.logger.Error("failed to read kafka event outbox", slog.Any("error", err))
			f.metrics.recordReplayFailure()
			if !f.waitRetry(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		if item == nil {
			select {
			case <-f.ctx.Done():
				return
			case <-f.wake:
			}
			continue
		}
		if producer == nil {
			producer, err = f.newProducer()
			if err != nil {
				f.logger.Warn("kafka unavailable; durable outbox retained", slog.Any("error", err))
				f.metrics.recordReplayFailure()
				if !f.waitRetry(backoff) {
					return
				}
				backoff = nextBackoff(backoff)
				continue
			}
		}
		started := time.Now()
		_, _, err = producer.SendMessage(&sarama.ProducerMessage{Topic: f.topic, Key: sarama.StringEncoder(item.record.Key), Value: sarama.ByteEncoder(item.record.Value)})
		if err != nil {
			f.logger.Warn("kafka publish failed; durable outbox retained", slog.Any("error", err), slog.String("event_id", item.record.EventID))
			f.metrics.recordReplayFailure()
			_ = producer.Close()
			producer = nil
			if !f.waitRetry(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		if err = f.outbox.acknowledge(item.sequence); err != nil {
			f.logger.Error("failed to acknowledge kafka event outbox record", slog.Any("error", err), slog.String("event_id", item.record.EventID))
			f.metrics.recordReplayFailure()
			if !f.waitRetry(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		f.metrics.recordAcknowledged(time.Since(started))
		backoff = defaultRetryMin
	}
}

func (f *kafkaFactory) waitRetry(base time.Duration) bool {
	jitter := time.Duration(rand.Float64() * float64(base) / 2)
	timer := time.NewTimer(base + jitter)
	defer timer.Stop()
	select {
	case <-f.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current >= defaultRetryMax/2 {
		return defaultRetryMax
	}
	return current * 2
}

func (f *kafkaFactory) signalShipper() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *kafkaFactory) recordAppendFailure(stage string, err error, eventID string) {
	f.logger.Error("failed to append kafka event to durable outbox", slog.String("stage", stage), slog.Any("error", err), slog.String("event_id", eventID))
	f.metrics.recordAppendFailure()
	if errors.Is(err, errOutboxFull) {
		f.metrics.recordDropped()
	}
}
