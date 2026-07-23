// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingBodyStore struct {
	maxBytes int64
	objects  []BodyObject
	err      error
}

func (s *recordingBodyStore) MaxBodyBytes() int64 { return s.maxBytes }

func (s *recordingBodyStore) Put(_ context.Context, object BodyObject) (*BodyObjectReference, error) {
	object.Body = append([]byte(nil), object.Body...)
	s.objects = append(s.objects, object)
	if s.err != nil {
		return nil, s.err
	}
	return &BodyObjectReference{Provider: "s3", Bucket: "audit", Key: object.Kind + ".bin"}, nil
}

func TestPublisherStateStoresTruncatedBodies(t *testing.T) {
	store := &recordingBodyStore{maxBytes: 1024}
	state := newPublisherStateWithBodyStore("chat", 4, store)
	request := []byte("request-body")
	response := []byte("response-body")
	state.SetRequestID("req-1")
	state.SetRequestBody(request)
	state.SetUpstreamRequestBody(request)
	state.ObserveResponseBody(response)

	event := state.buildEvent(true, "", nil, 1, 0, 0, nil)
	failures := state.storeExternalBodies(t.Context(), event)

	require.Empty(t, failures)
	require.Len(t, store.objects, 2, "unchanged upstream body must not be uploaded twice")
	require.Equal(t, "request", store.objects[0].Kind)
	require.Equal(t, request, store.objects[0].Body)
	require.Equal(t, "response", store.objects[1].Kind)
	require.Equal(t, response, store.objects[1].Body)
	require.Equal(t, "request.bin", event.RequestBody.Object.Key)
	require.Equal(t, "request_body", event.UpstreamRequestBody.SameAs)
	require.Nil(t, event.UpstreamRequestBody.Object)
	require.Equal(t, "response.bin", event.ResponseBody.Object.Key)
	require.Equal(t, &ExternalStorePolicy{Provider: "s3", MaxBodyBytes: 1024}, event.CapturePolicy.ExternalBodyStore)
}

func TestPublisherStateReportsExternalBodyFailure(t *testing.T) {
	t.Run("upload failure", func(t *testing.T) {
		store := &recordingBodyStore{maxBytes: 1024, err: errors.New("storage unavailable")}
		state := newPublisherStateWithBodyStore("chat", 1, store)
		state.SetRequestBody([]byte("body"))
		event := state.buildEvent(true, "", nil, 1, 0, 0, nil)

		failures := state.storeExternalBodies(t.Context(), event)
		require.Len(t, failures, 1)
		require.Equal(t, externalStorageUploadFailed, event.RequestBody.ExternalStorageError)
		require.Nil(t, event.RequestBody.Object)
	})

	t.Run("body exceeds external limit", func(t *testing.T) {
		store := &recordingBodyStore{maxBytes: 3}
		state := newPublisherStateWithBodyStore("chat", 1, store)
		state.SetRequestBody([]byte("body"))
		event := state.buildEvent(true, "", nil, 1, 0, 0, nil)

		failures := state.storeExternalBodies(t.Context(), event)
		require.Len(t, failures, 1)
		require.Empty(t, store.objects)
		require.Equal(t, externalStorageBodyTooLarge, event.RequestBody.ExternalStorageError)
	})
}
