package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type KafkaRESTConfig struct {
	URL                 string
	Topic               string
	BodyCaptureMaxBytes int
	BodyStore           BodyStore
}

type kafkaRESTFactory struct {
	client              *http.Client
	url                 string
	topic               string
	headerKeys          map[string]bool
	bodyCaptureMaxBytes int
	bodyStore           BodyStore
	logger              *slog.Logger
	publishWG           sync.WaitGroup
}

func NewKafkaRESTFactory(cfg KafkaRESTConfig, headerKeys []string, logger *slog.Logger) (Factory, func(), error) {
	if cfg.URL == "" {
		return nil, nil, fmt.Errorf("kafka REST URL is required")
	}

	hk := make(map[string]bool, len(headerKeys))
	for _, k := range headerKeys {
		if k = strings.TrimSpace(strings.ToLower(k)); k != "" {
			hk[k] = true
		}
	}

	f := &kafkaRESTFactory{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		url:                 cfg.URL,
		topic:               cfg.Topic,
		headerKeys:          hk,
		bodyCaptureMaxBytes: cfg.BodyCaptureMaxBytes,
		bodyStore:           cfg.BodyStore,
		logger:              logger,
	}

	shutdown := func() {
		f.publishWG.Wait()
		f.client.CloseIdleConnections()
	}

	return f, shutdown, nil
}

func (f *kafkaRESTFactory) NewPublisher(operation string) Publisher {
	return &kafkaRESTPublisher{
		factory:        f,
		publisherState: newPublisherStateWithBodyStore(operation, f.bodyCaptureMaxBytes, f.bodyStore),
	}
}

type kafkaRESTPublisher struct {
	factory *kafkaRESTFactory
	*publisherState
}

type kafkaRESTRecord struct {
	Key   string          `json:"key,omitempty"`
	Value json.RawMessage `json:"value"`
}

type kafkaRESTPayload struct {
	Records []kafkaRESTRecord `json:"records"`
}

func (p *kafkaRESTPublisher) Publish(_ context.Context, success bool, errorType string, tokens *TokenInfo, latencyMs, ttftMs, itlMs float64) {
	event := p.buildEvent(success, errorType, tokens, latencyMs, ttftMs, itlMs, p.factory.headerKeys)
	p.factory.publishWG.Add(1)
	go func() {
		defer p.factory.publishWG.Done()
		for _, failure := range p.storeExternalBodies(context.Background(), event) {
			p.factory.logger.Error("failed to store complete event body",
				slog.Any("error", failure.err), slog.String("body_kind", failure.kind), slog.String("event_id", event.EventID))
		}
		p.publishEvent(event)
	}()
}

func (p *kafkaRESTPublisher) publishEvent(event *RequestEvent) {
	eventData, err := json.Marshal(event)
	if err != nil {
		p.factory.logger.Error("failed to marshal event", slog.Any("error", err))
		return
	}

	payload := kafkaRESTPayload{
		Records: []kafkaRESTRecord{
			{
				Key:   event.RequestID,
				Value: eventData,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		p.factory.logger.Error("failed to marshal kafka REST payload", slog.Any("error", err))
		return
	}

	url := fmt.Sprintf("%s/topics/%s", p.factory.url, p.factory.topic)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		p.factory.logger.Error("failed to create kafka REST request", slog.Any("error", err))
		return
	}
	req.Header.Set("Content-Type", "application/vnd.kafka.json.v2+json")
	req.Header.Set("Accept", "application/vnd.kafka.v2+json")

	resp, err := p.factory.client.Do(req)
	if err != nil {
		p.factory.logger.Error("failed to publish event via kafka REST", slog.Any("error", err))
		return
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		p.factory.logger.Error("kafka REST publish failed",
			slog.Int("status", resp.StatusCode),
			slog.String("body", string(respBody)),
		)
	}
}
