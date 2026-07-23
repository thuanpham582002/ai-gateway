// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestS3BodyStoreSupportsCompatibleEndpointAndCustomCA(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "/audit-bucket/audit/2026/07/23/event-1/request.bin", r.URL.Path)
		require.Equal(t, "event-1", r.Header.Get("X-Amz-Meta-Event-Id"))
		require.Equal(t, "sha", r.Header.Get("X-Amz-Meta-Sha256"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		requestBody <- body
		w.Header().Set("ETag", `"etag-1"`)
		w.Header().Set("X-Amz-Version-Id", "version-1")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	store, shutdown, err := NewS3BodyStore(t.Context(), S3BodyStoreConfig{
		Endpoint:      server.URL,
		Bucket:        "audit-bucket",
		Region:        "us-east-1",
		Prefix:        "audit",
		CAPEM:         caPEM,
		UsePathStyle:  true,
		MaxBodyBytes:  1024,
		UploadTimeout: time.Second,
	})
	require.NoError(t, err)
	defer shutdown()

	reference, err := store.Put(t.Context(), BodyObject{
		EventID:   "event-1",
		RequestID: "request-1",
		Kind:      "request",
		Timestamp: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		SHA256:    "sha256:sha",
		Body:      []byte("complete body"),
	})
	require.NoError(t, err)
	require.Equal(t, []byte("complete body"), <-requestBody)
	require.Equal(t, &BodyObjectReference{
		Provider: "s3", Bucket: "audit-bucket", Key: "audit/2026/07/23/event-1/request.bin",
		ETag: "etag-1", VersionID: "version-1",
	}, reference)
}

func TestS3BodyStoreLoadsCAFileAndValidatesConfiguration(t *testing.T) {
	t.Run("CA file", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		caPath := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(caPath,
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
		t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
		t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

		store, shutdown, err := NewS3BodyStore(t.Context(), S3BodyStoreConfig{
			Endpoint: server.URL, Bucket: "bucket", CABundlePath: caPath,
			UsePathStyle: true, MaxBodyBytes: 1, UploadTimeout: time.Second,
		})
		require.NoError(t, err)
		shutdown()
		require.NotNil(t, store)
	})

	for _, test := range []struct {
		name string
		cfg  S3BodyStoreConfig
	}{
		{name: "missing bucket", cfg: S3BodyStoreConfig{MaxBodyBytes: 1}},
		{name: "invalid endpoint", cfg: S3BodyStoreConfig{Bucket: "bucket", Endpoint: "file:///tmp/s3", MaxBodyBytes: 1}},
		{name: "invalid CA", cfg: S3BodyStoreConfig{Bucket: "bucket", CAPEM: "invalid", MaxBodyBytes: 1}},
		{name: "invalid encryption", cfg: S3BodyStoreConfig{Bucket: "bucket", MaxBodyBytes: 1, ServerSideEncryption: "invalid"}},
		{name: "KMS key without KMS", cfg: S3BodyStoreConfig{Bucket: "bucket", MaxBodyBytes: 1, KMSKeyID: "key"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := NewS3BodyStore(t.Context(), test.cfg)
			require.Error(t, err)
		})
	}
}
