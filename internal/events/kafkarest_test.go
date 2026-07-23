// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKafkaRESTPublisherStoresBodyBeforePublishingReference(t *testing.T) {
	received := make(chan RequestEvent, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/topics/audit", r.URL.Path)
		var payload kafkaRESTPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Len(t, payload.Records, 1)
		var event RequestEvent
		require.NoError(t, json.Unmarshal(payload.Records[0].Value, &event))
		received <- event
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := &recordingBodyStore{maxBytes: 1024}
	factory, shutdown, err := NewKafkaRESTFactory(KafkaRESTConfig{
		URL: server.URL, Topic: "audit", BodyCaptureMaxBytes: 1, BodyStore: store,
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	publisher := factory.NewPublisher("chat")
	publisher.SetRequestID("req-rest-s3")
	publisher.SetRequestBody([]byte("complete request"))
	publisher.Publish(t.Context(), true, "", nil, 1, 0, 0)
	shutdown()

	event := <-received
	require.Len(t, store.objects, 1)
	require.Equal(t, "request.bin", event.RequestBody.Object.Key)
	require.Empty(t, event.RequestBody.ExternalStorageError)
}
