package events

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	kafkaMetricEventsAttempted      = "ai_gateway.kafka.events.attempted"
	kafkaMetricEventsEnqueued       = "ai_gateway.kafka.events.enqueued"
	kafkaMetricEventsPublished      = "ai_gateway.kafka.events.published"
	kafkaMetricEventsFailed         = "ai_gateway.kafka.events.failed"
	kafkaMetricEventPublishDuration = "ai_gateway.kafka.event.publish.duration"
)

// KafkaMetrics records AI Gateway Kafka event-publishing telemetry.
// Labels are intentionally low-cardinality: topic, operation, transport, and error_type.
type KafkaMetrics struct {
	attempted       metric.Int64Counter
	enqueued        metric.Int64Counter
	published       metric.Int64Counter
	failed          metric.Int64Counter
	publishDuration metric.Float64Histogram
}

// NewKafkaMetrics creates metrics for Kafka event publishing.
func NewKafkaMetrics(meter metric.Meter) (*KafkaMetrics, error) {
	attempted, err := meter.Int64Counter(
		kafkaMetricEventsAttempted,
		metric.WithDescription("Total number of AI Gateway Kafka event publish attempts."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	enqueued, err := meter.Int64Counter(
		kafkaMetricEventsEnqueued,
		metric.WithDescription("Total number of AI Gateway Kafka events enqueued to an async producer."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	published, err := meter.Int64Counter(
		kafkaMetricEventsPublished,
		metric.WithDescription("Total number of AI Gateway Kafka events successfully published."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	failed, err := meter.Int64Counter(
		kafkaMetricEventsFailed,
		metric.WithDescription("Total number of AI Gateway Kafka event publish failures."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	publishDuration, err := meter.Float64Histogram(
		kafkaMetricEventPublishDuration,
		metric.WithDescription("AI Gateway Kafka event publish duration."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10),
	)
	if err != nil {
		return nil, err
	}
	return &KafkaMetrics{
		attempted:       attempted,
		enqueued:        enqueued,
		published:       published,
		failed:          failed,
		publishDuration: publishDuration,
	}, nil
}

func (m *KafkaMetrics) recordAttempt(ctx context.Context, topic, operation, transport string) {
	if m == nil {
		return
	}
	m.attempted.Add(ctx, 1, metric.WithAttributeSet(kafkaAttributeSet(topic, operation, transport)))
}

func (m *KafkaMetrics) recordEnqueued(ctx context.Context, topic, operation, transport string) {
	if m == nil {
		return
	}
	m.enqueued.Add(ctx, 1, metric.WithAttributeSet(kafkaAttributeSet(topic, operation, transport)))
}

func (m *KafkaMetrics) recordPublished(ctx context.Context, topic, operation, transport string, startedAt time.Time) {
	if m == nil {
		return
	}
	attrs := kafkaAttributeSet(topic, operation, transport)
	m.published.Add(ctx, 1, metric.WithAttributeSet(attrs))
	if !startedAt.IsZero() {
		m.publishDuration.Record(ctx, time.Since(startedAt).Seconds(), metric.WithAttributeSet(attrs))
	}
}

func (m *KafkaMetrics) recordFailed(ctx context.Context, topic, operation, transport, errorType string, startedAt time.Time) {
	if m == nil {
		return
	}
	attrs := kafkaAttributeSet(topic, operation, transport, attribute.String("error.type", errorType))
	m.failed.Add(ctx, 1, metric.WithAttributeSet(attrs))
	if !startedAt.IsZero() {
		m.publishDuration.Record(ctx, time.Since(startedAt).Seconds(), metric.WithAttributeSet(attrs))
	}
}

func kafkaAttributeSet(topic, operation, transport string, extra ...attribute.KeyValue) attribute.Set {
	attrs := []attribute.KeyValue{
		attribute.String("kafka.topic", topic),
		attribute.String("ai_gateway.operation", operation),
		attribute.String("ai_gateway.kafka.transport", transport),
	}
	attrs = append(attrs, extra...)
	return attribute.NewSet(attrs...)
}
