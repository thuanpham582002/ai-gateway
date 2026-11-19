// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogSafeMultimodalRequest_DataURLIncludesSizeWithoutPayload(t *testing.T) {
	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	body := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + pngBase64 + `"}}]}]}`)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logSafeMultimodalRequest(logger, body)
	got := buf.String()

	require.Contains(t, got, `msg="multimodal request image"`)
	require.Contains(t, got, "scheme=data")
	require.Contains(t, got, "mime=image/png")
	require.Contains(t, got, "encoded_len=92")
	require.Contains(t, got, "decoded_bytes_estimate=68")
	require.Contains(t, got, "format=png")
	require.Contains(t, got, "width=1")
	require.Contains(t, got, "height=1")
	require.Contains(t, got, "pixels=1")
	require.Contains(t, got, "image_count=1")
	require.NotContains(t, got, pngBase64)
	require.NotContains(t, got, "data:image/png;base64")
}

func TestLogSafeMultimodalRequest_RemoteURLIsHashedNotLogged(t *testing.T) {
	const imageURL = "https://example.internal/private/image.png?sig=secret"
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]}]}`)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logSafeMultimodalRequest(logger, body)
	got := buf.String()

	require.Contains(t, got, "scheme=https")
	require.Contains(t, got, "sha256=")
	require.NotContains(t, got, imageURL)
	require.NotContains(t, got, "secret")
	require.NotContains(t, got, "encoded_len=")
	require.NotContains(t, got, "width=")
}

func TestLogSafeMultimodalRequest_TextOnlyDoesNotLog(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logSafeMultimodalRequest(logger, body)
	require.Empty(t, buf.String())
}
