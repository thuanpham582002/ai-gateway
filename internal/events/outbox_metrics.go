// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"
)

type outboxMetrics struct {
	enqueued           metric.Int64Counter
	acknowledged       metric.Int64Counter
	appendFailures     metric.Int64Counter
	replayFailures     metric.Int64Counter
	dropped            metric.Int64Counter
	ackLatency         metric.Float64Histogram
	noopEnqueued       atomic.Int64
	noopAcknowledged   atomic.Int64
	noopAppendFailures atomic.Int64
	noopReplayFailures atomic.Int64
	noopDropped        atomic.Int64
}

func newOutboxMetrics(meter metric.Meter, outbox *durableOutbox) *outboxMetrics {
	m := &outboxMetrics{}
	if meter == nil {
		return m
	}
	m.enqueued, _ = meter.Int64Counter("aigateway.kafka.outbox.enqueued")
	m.acknowledged, _ = meter.Int64Counter("aigateway.kafka.outbox.acknowledged")
	m.appendFailures, _ = meter.Int64Counter("aigateway.kafka.outbox.append_errors")
	m.replayFailures, _ = meter.Int64Counter("aigateway.kafka.outbox.replay_errors")
	m.dropped, _ = meter.Int64Counter("aigateway.kafka.outbox.dropped")
	m.ackLatency, _ = meter.Float64Histogram("aigateway.kafka.ack.latency", metric.WithUnit("ms"))
	_, _ = meter.Int64ObservableGauge("aigateway.kafka.outbox.records", metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
		records, _ := outbox.stats()
		observer.Observe(records)
		return nil
	}))
	_, _ = meter.Int64ObservableGauge("aigateway.kafka.outbox.bytes", metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
		_, bytes := outbox.stats()
		observer.Observe(bytes)
		return nil
	}))
	_, _ = meter.Float64ObservableGauge("aigateway.kafka.outbox.oldest_record_age", metric.WithUnit("s"), metric.WithFloat64Callback(func(_ context.Context, observer metric.Float64Observer) error {
		observer.Observe(float64(outbox.oldestReadyAge(time.Now())) / float64(time.Second))
		return nil
	}))
	return m
}

func (m *outboxMetrics) recordEnqueued() {
	if m.enqueued != nil {
		m.enqueued.Add(context.Background(), 1)
	} else {
		m.noopEnqueued.Add(1)
	}
}

func (m *outboxMetrics) recordAcknowledged(latency time.Duration) {
	if m.acknowledged != nil {
		m.acknowledged.Add(context.Background(), 1)
		m.ackLatency.Record(context.Background(), float64(latency)/float64(time.Millisecond))
	} else {
		m.noopAcknowledged.Add(1)
	}
}

func (m *outboxMetrics) recordAppendFailure() {
	if m.appendFailures != nil {
		m.appendFailures.Add(context.Background(), 1)
	} else {
		m.noopAppendFailures.Add(1)
	}
}

func (m *outboxMetrics) recordReplayFailure() {
	if m.replayFailures != nil {
		m.replayFailures.Add(context.Background(), 1)
	} else {
		m.noopReplayFailures.Add(1)
	}
}

func (m *outboxMetrics) recordDropped() {
	if m.dropped != nil {
		m.dropped.Add(context.Background(), 1)
	} else {
		m.noopDropped.Add(1)
	}
}
