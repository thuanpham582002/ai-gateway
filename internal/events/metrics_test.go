package events

import (
	"testing"
	"time"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
)

func TestKafkaMetrics(t *testing.T) {
	t.Parallel()

	reader := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(reader)).Meter("test")
	km, err := NewKafkaMetrics(meter)
	require.NoError(t, err)
	require.NotNil(t, km)

	attrs := kafkaAttributeSet("ai-gateway-events", "chat", "sarama")
	failedAttrs := kafkaAttributeSet("ai-gateway-events", "chat", "sarama", attribute.String("error.type", "producer_error"))

	startedAt := time.Now().Add(-10 * time.Millisecond)
	km.recordAttempt(t.Context(), "ai-gateway-events", "chat", "sarama")
	km.recordEnqueued(t.Context(), "ai-gateway-events", "chat", "sarama")
	km.recordPublished(t.Context(), "ai-gateway-events", "chat", "sarama", startedAt)
	km.recordFailed(t.Context(), "ai-gateway-events", "chat", "sarama", "producer_error", startedAt)

	require.Equal(t, int64(1), internaltesting.GetInt64CounterValue(t, reader, kafkaMetricEventsAttempted, attrs))
	require.Equal(t, int64(1), internaltesting.GetInt64CounterValue(t, reader, kafkaMetricEventsEnqueued, attrs))
	require.Equal(t, int64(1), internaltesting.GetInt64CounterValue(t, reader, kafkaMetricEventsPublished, attrs))
	require.Equal(t, int64(1), internaltesting.GetInt64CounterValue(t, reader, kafkaMetricEventsFailed, failedAttrs))

	count, sum := internaltesting.GetHistogramValues(t, reader, kafkaMetricEventPublishDuration, attrs)
	require.Equal(t, uint64(1), count)
	require.Greater(t, sum, 0.0)

	count, sum = internaltesting.GetHistogramValues(t, reader, kafkaMetricEventPublishDuration, failedAttrs)
	require.Equal(t, uint64(1), count)
	require.Greater(t, sum, 0.0)
}
