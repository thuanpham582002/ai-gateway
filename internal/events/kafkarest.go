package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type KafkaRESTConfig struct {
	URL   string
	Topic string
}

type kafkaRESTFactory struct {
	client     *http.Client
	url        string
	topic      string
	headerKeys map[string]bool
	logger     *slog.Logger
	metrics    *KafkaMetrics
}

func NewKafkaRESTFactory(cfg KafkaRESTConfig, headerKeys []string, logger *slog.Logger, kafkaMetrics *KafkaMetrics) (Factory, func(), error) {
	if cfg.URL == "" {
		return nil, nil, fmt.Errorf("kafka REST URL is required")
	}

	hk := make(map[string]bool, len(headerKeys))
	for _, k := range headerKeys {
		if k != "" {
			hk[k] = true
		}
	}

	f := &kafkaRESTFactory{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		url:        cfg.URL,
		topic:      cfg.Topic,
		headerKeys: hk,
		logger:     logger,
		metrics:    kafkaMetrics,
	}

	shutdown := func() {
		f.client.CloseIdleConnections()
	}

	return f, shutdown, nil
}

func (f *kafkaRESTFactory) NewPublisher(operation string) Publisher {
	return &kafkaRESTPublisher{factory: f, operation: operation}
}

type kafkaRESTPublisher struct {
	factory           *kafkaRESTFactory
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

func (p *kafkaRESTPublisher) SetRequestID(id string)                      { p.requestID = id }
func (p *kafkaRESTPublisher) SetOriginalModel(model string)               { p.originalModel = model }
func (p *kafkaRESTPublisher) SetRequestModel(model string)                { p.requestModel = model }
func (p *kafkaRESTPublisher) SetResponseModel(model string)               { p.responseModel = model }
func (p *kafkaRESTPublisher) SetBackend(backend string)                   { p.backend = backend }
func (p *kafkaRESTPublisher) SetBackendName(name string)                  { p.backendName = name }
func (p *kafkaRESTPublisher) SetSelectedPool(pool string)                 { p.selectedPool = pool }
func (p *kafkaRESTPublisher) SetModelNameOverride(override string)        { p.modelNameOverride = override }
func (p *kafkaRESTPublisher) SetStream(stream bool)                       { p.stream = stream }
func (p *kafkaRESTPublisher) SetRequestHeaders(headers map[string]string) { p.headers = headers }

type kafkaRESTRecord struct {
	Key   string          `json:"key,omitempty"`
	Value json.RawMessage `json:"value"`
}

type kafkaRESTPayload struct {
	Records []kafkaRESTRecord `json:"records"`
}

func (p *kafkaRESTPublisher) Publish(_ context.Context, success bool, errorType string, tokens *TokenInfo, latencyMs, ttftMs, itlMs float64) {
	eventType := "request_completed"
	if !success {
		eventType = "request_failed"
	}

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

	ctx := context.Background()
	p.factory.metrics.recordAttempt(ctx, p.factory.topic, p.operation, "rest")
	startedAt := time.Now()

	eventData, err := json.Marshal(event)
	if err != nil {
		p.factory.metrics.recordFailed(ctx, p.factory.topic, p.operation, "rest", "marshal_error", startedAt)
		p.factory.logger.Error("failed to marshal event", slog.Any("error", err))
		return
	}

	payload := kafkaRESTPayload{
		Records: []kafkaRESTRecord{
			{
				Key:   p.requestID,
				Value: eventData,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		p.factory.metrics.recordFailed(ctx, p.factory.topic, p.operation, "rest", "marshal_payload_error", startedAt)
		p.factory.logger.Error("failed to marshal kafka REST payload", slog.Any("error", err))
		return
	}

	url := fmt.Sprintf("%s/topics/%s", p.factory.url, p.factory.topic)

	go func() {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			p.factory.metrics.recordFailed(context.Background(), p.factory.topic, p.operation, "rest", "request_create_error", startedAt)
			p.factory.logger.Error("failed to create kafka REST request", slog.Any("error", err))
			return
		}
		req.Header.Set("Content-Type", "application/vnd.kafka.json.v2+json")
		req.Header.Set("Accept", "application/vnd.kafka.v2+json")

		resp, err := p.factory.client.Do(req)
		if err != nil {
			p.factory.metrics.recordFailed(context.Background(), p.factory.topic, p.operation, "rest", "request_error", startedAt)
			p.factory.logger.Error("failed to publish event via kafka REST", slog.Any("error", err))
			return
		}
		defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

		if resp.StatusCode >= 400 {
			p.factory.metrics.recordFailed(context.Background(), p.factory.topic, p.operation, "rest", "http_error", startedAt)
			respBody, _ := io.ReadAll(resp.Body)
			p.factory.logger.Error("kafka REST publish failed",
				slog.Int("status", resp.StatusCode),
				slog.String("body", string(respBody)),
			)
			return
		}
		p.factory.metrics.recordPublished(context.Background(), p.factory.topic, p.operation, "rest", startedAt)
	}()
}
