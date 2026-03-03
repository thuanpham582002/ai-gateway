// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import "go.opentelemetry.io/otel/metric"

// mustRegisterCounter registers a Float64Counter with the meter and panics if it fails.
func mustRegisterCounter(meter metric.Meter, name string, options ...metric.Float64CounterOption) metric.Float64Counter {
	h, err := meter.Float64Counter(name, options...)
	if err != nil {
		panic(err)
	}
	return h
}

// mustRegisterInt64Counter registers an Int64Counter with the meter and panics if it fails.
func mustRegisterInt64Counter(meter metric.Meter, name string, options ...metric.Int64CounterOption) metric.Int64Counter {
	h, err := meter.Int64Counter(name, options...)
	if err != nil {
		panic(err)
	}
	return h
}

// mustRegisterHistogram registers a histogram with the meter and panics if it fails.
func mustRegisterHistogram(meter metric.Meter, name string, options ...metric.Float64HistogramOption) metric.Float64Histogram {
	h, err := meter.Float64Histogram(name, options...)
	if err != nil {
		panic(err)
	}
	return h
}
