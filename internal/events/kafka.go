// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"time"

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
}

// kafkaFactory implements Factory using sarama.AsyncProducer.
type kafkaFactory struct {
	producer   sarama.AsyncProducer
	topic      string
	headerKeys map[string]bool // which request headers to include in events
	logger     *slog.Logger
}

// NewKafkaFactory creates a Factory backed by Kafka.
// Returns the factory, a shutdown function to flush and close the producer, and any error.
func NewKafkaFactory(cfg KafkaConfig, headerKeys []string, logger *slog.Logger) (Factory, func(), error) {
	saramaCfg := sarama.NewConfig()
	saramaCfg.Producer.Return.Errors = true
	saramaCfg.Producer.Return.Successes = false // we don't need success confirmations
	saramaCfg.Producer.Compression = sarama.CompressionSnappy
	saramaCfg.Producer.RequiredAcks = sarama.WaitForLocal

	if cfg.TLSEnabled {
		saramaCfg.Net.TLS.Enable = true
		saramaCfg.Net.TLS.Config = &tls.Config{MinVersion: tls.VersionTLS12} // #nosec G402
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

	producer, err := sarama.NewAsyncProducer(cfg.Brokers, saramaCfg)
	if err != nil {
		return nil, nil, err
	}

	hk := make(map[string]bool, len(headerKeys))
	for _, k := range headerKeys {
		if k != "" {
			hk[k] = true
		}
	}

	f := &kafkaFactory{
		producer:   producer,
		topic:      cfg.Topic,
		headerKeys: hk,
		logger:     logger,
	}

	// Drain error channel in background.
	go func() {
		for err := range producer.Errors() {
			f.logger.Error("kafka producer error", slog.Any("error", err.Err))
		}
	}()

	shutdown := func() {
		if err := producer.Close(); err != nil {
			f.logger.Error("failed to close kafka producer", slog.Any("error", err))
		}
	}

	return f, shutdown, nil
}

func (f *kafkaFactory) NewPublisher(operation string) Publisher {
	return &kafkaPublisher{factory: f, operation: operation}
}

// kafkaPublisher implements Publisher for a single request.
type kafkaPublisher struct {
	factory           *kafkaFactory
	requestID         string
	operation         string
	originalModel     string
	requestModel      string
	responseModel     string
	backend           string
	backendName       string
	selectedPool      string
	modelNameOverride string
	stream            bool
	headers           map[string]string
}

func (p *kafkaPublisher) SetRequestID(id string)            { p.requestID = id }
func (p *kafkaPublisher) SetOriginalModel(model string)      { p.originalModel = model }
func (p *kafkaPublisher) SetRequestModel(model string)       { p.requestModel = model }
func (p *kafkaPublisher) SetResponseModel(model string)      { p.responseModel = model }
func (p *kafkaPublisher) SetBackend(backend string)          { p.backend = backend }
func (p *kafkaPublisher) SetBackendName(name string)         { p.backendName = name }
func (p *kafkaPublisher) SetSelectedPool(pool string)        { p.selectedPool = pool }
func (p *kafkaPublisher) SetModelNameOverride(override string) { p.modelNameOverride = override }
func (p *kafkaPublisher) SetStream(stream bool)              { p.stream = stream }
func (p *kafkaPublisher) SetRequestHeaders(headers map[string]string) { p.headers = headers }

// Publish emits the accumulated event to Kafka asynchronously.
func (p *kafkaPublisher) Publish(_ context.Context, success bool, errorType string, tokens *TokenInfo, latencyMs, ttftMs, itlMs float64) {
	eventType := "request_completed"
	if !success {
		eventType = "request_failed"
	}

	// Include headers: if specific keys configured, filter; otherwise include all.
	var filteredHeaders map[string]string
	if len(p.headers) > 0 {
		if len(p.factory.headerKeys) > 0 {
			filteredHeaders = make(map[string]string, len(p.factory.headerKeys))
			for k := range p.factory.headerKeys {
				if v, ok := p.headers[k]; ok {
					filteredHeaders[k] = v
				}
			}
		} else {
			filteredHeaders = make(map[string]string, len(p.headers))
			for k, v := range p.headers {
				filteredHeaders[k] = v
			}
		}
	}

	event := &RequestEvent{
		EventType:           eventType,
		Timestamp:           time.Now(),
		RequestID:           p.requestID,
		Operation:           p.operation,
		OriginalModel:       p.originalModel,
		RequestModel:        p.requestModel,
		ResponseModel:       p.responseModel,
		Backend:             p.backend,
		BackendName:         p.backendName,
		Success:             success,
		ErrorType:           errorType,
		LatencyMs:           latencyMs,
		Tokens:              tokens,
		Stream:              p.stream,
		TimeToFirstTokenMs:  ttftMs,
		InterTokenLatencyMs: itlMs,
		Headers:             filteredHeaders,
		SelectedPool:        p.selectedPool,
		ModelNameOverride:   p.modelNameOverride,
	}

	data, err := json.Marshal(event)
	if err != nil {
		p.factory.logger.Error("failed to marshal event", slog.Any("error", err))
		return
	}

	p.factory.producer.Input() <- &sarama.ProducerMessage{
		Topic: p.factory.topic,
		Key:   sarama.StringEncoder(p.requestID),
		Value: sarama.ByteEncoder(data),
	}
}
