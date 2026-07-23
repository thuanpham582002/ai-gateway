// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/IBM/sarama"
)

// KafkaConfig holds Kafka producer configuration.
type KafkaConfig struct {
	Brokers       []string
	Topic         string
	SASLUser      string
	SASLPassword  string
	SASLMechanism string // "PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"
	TLSEnabled    bool
	TLSCABundle   string
	TLSCAPEM      string
	// BodyCaptureMaxBytes controls inline capture per request/response body. Zero stores hashes only.
	BodyCaptureMaxBytes int
	// SpoolDir persists unacknowledged events for replay after a producer process restart.
	SpoolDir string
	// BodyStore receives complete bodies that exceed BodyCaptureMaxBytes.
	BodyStore BodyStore
}

// kafkaFactory implements Factory using sarama.AsyncProducer.
type kafkaFactory struct {
	producer            sarama.AsyncProducer
	topic               string
	headerKeys          map[string]bool // which request headers to include in events
	bodyCaptureMaxBytes int
	spoolDir            string
	bodyStore           BodyStore
	logger              *slog.Logger
	publishWG           sync.WaitGroup
}

// NewKafkaFactory creates a Factory backed by Kafka.
// Returns the factory, a shutdown function to flush and close the producer, and any error.
func NewKafkaFactory(cfg KafkaConfig, headerKeys []string, logger *slog.Logger) (Factory, func(), error) {
	saramaCfg := sarama.NewConfig()
	saramaCfg.Producer.Return.Errors = true
	saramaCfg.Producer.Return.Successes = true
	saramaCfg.Producer.Compression = sarama.CompressionSnappy
	saramaCfg.Producer.RequiredAcks = sarama.WaitForAll
	saramaCfg.Producer.Idempotent = true
	saramaCfg.Producer.Retry.Max = 5
	saramaCfg.Net.MaxOpenRequests = 1

	if cfg.TLSEnabled {
		tlsConfig, err := tlsConfigWithAdditionalCA(cfg.TLSCABundle, cfg.TLSCAPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("configure Kafka TLS: %w", err)
		}
		saramaCfg.Net.TLS.Enable = true
		saramaCfg.Net.TLS.Config = tlsConfig
	}

	if cfg.SASLUser != "" {
		saramaCfg.Net.SASL.Enable = true
		saramaCfg.Net.SASL.User = cfg.SASLUser
		saramaCfg.Net.SASL.Password = cfg.SASLPassword
		switch cfg.SASLMechanism {
		case "SCRAM-SHA-256":
			saramaCfg.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA256
			saramaCfg.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA256} }
		case "SCRAM-SHA-512":
			saramaCfg.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA512
			saramaCfg.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA512} }
		default:
			saramaCfg.Net.SASL.Mechanism = sarama.SASLTypePlaintext
		}
	}

	if err := prepareSpoolDir(cfg.SpoolDir); err != nil {
		return nil, nil, err
	}

	producer, err := sarama.NewAsyncProducer(cfg.Brokers, saramaCfg)
	if err != nil {
		return nil, nil, err
	}

	hk := make(map[string]bool, len(headerKeys))
	for _, k := range headerKeys {
		if k = strings.TrimSpace(strings.ToLower(k)); k != "" {
			hk[k] = true
		}
	}

	f := &kafkaFactory{
		producer:            producer,
		topic:               cfg.Topic,
		headerKeys:          hk,
		bodyCaptureMaxBytes: cfg.BodyCaptureMaxBytes,
		spoolDir:            cfg.SpoolDir,
		bodyStore:           cfg.BodyStore,
		logger:              logger,
	}

	go f.drainProducerResults()
	if err := f.replaySpooledEvents(); err != nil {
		_ = producer.Close()
		return nil, nil, err
	}

	shutdown := func() {
		f.publishWG.Wait()
		if err := producer.Close(); err != nil {
			f.logger.Error("failed to close kafka producer", slog.Any("error", err))
		}
	}

	return f, shutdown, nil
}

func (f *kafkaFactory) NewPublisher(operation string) Publisher {
	return &kafkaPublisher{
		factory:        f,
		publisherState: newPublisherStateWithBodyStore(operation, f.bodyCaptureMaxBytes, f.bodyStore),
	}
}

// kafkaPublisher implements Publisher for a single request.
type kafkaPublisher struct {
	factory *kafkaFactory
	*publisherState
}

// Publish emits the accumulated event to Kafka asynchronously.
func (p *kafkaPublisher) Publish(_ context.Context, success bool, errorType string, tokens *TokenInfo, latencyMs, ttftMs, itlMs float64) {
	event := p.buildEvent(success, errorType, tokens, latencyMs, ttftMs, itlMs, p.factory.headerKeys)
	if p.factory.bodyStore == nil {
		p.publishEvent(event, "")
		return
	}
	spoolPath := p.spoolPendingUpload(event)
	p.factory.publishWG.Add(1)
	go func() {
		defer p.factory.publishWG.Done()
		for _, failure := range p.storeExternalBodies(context.Background(), event) {
			p.factory.logger.Error("failed to store complete event body",
				slog.Any("error", failure.err), slog.String("body_kind", failure.kind), slog.String("event_id", event.EventID))
		}
		p.publishEvent(event, spoolPath)
	}()
}

func (p *kafkaPublisher) spoolPendingUpload(event *RequestEvent) string {
	if p.factory.spoolDir == "" {
		return ""
	}
	markExternalBodiesPending(event)
	data, err := json.Marshal(event)
	if err != nil {
		p.factory.logger.Error("failed to marshal pending S3 event", slog.Any("error", err))
		return ""
	}
	spoolPath, err := writeSpoolRecord(p.factory.spoolDir, event.EventID, event.RequestID, data)
	if err != nil {
		p.factory.logger.Error("failed to spool pending S3 event", slog.Any("error", err), slog.String("event_id", event.EventID))
		return ""
	}
	return spoolPath
}

func (p *kafkaPublisher) publishEvent(event *RequestEvent, spoolPath string) {
	data, err := json.Marshal(event)
	if err != nil {
		p.factory.logger.Error("failed to marshal event", slog.Any("error", err))
		return
	}

	message := &sarama.ProducerMessage{
		Topic: p.factory.topic,
		Key:   sarama.StringEncoder(event.RequestID),
		Value: sarama.ByteEncoder(data),
	}
	if p.factory.spoolDir != "" {
		finalSpoolPath, err := writeSpoolRecord(p.factory.spoolDir, event.EventID, event.RequestID, data)
		if err != nil {
			p.factory.logger.Error("failed to spool kafka event", slog.Any("error", err), slog.String("event_id", event.EventID))
		} else {
			spoolPath = finalSpoolPath
		}
	}
	if spoolPath != "" {
		message.Metadata = spoolPath
	}
	p.factory.producer.Input() <- message
}
