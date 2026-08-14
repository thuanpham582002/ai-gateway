// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

const maxImageHeaderProbeBytes int64 = 1 << 20

type multimodalTraceEnvelope struct {
	Messages []multimodalTraceMessage `json:"messages"`
}

type multimodalTraceMessage struct {
	Content json.RawMessage `json:"content"`
}

type multimodalTraceContentPart struct {
	Type     string `json:"type"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

// logSafeMultimodalRequest emits metadata-only INFO logs for OpenAI-style image_url
// content parts. It never logs the URL, base64 payload, or image bytes. The raw
// client body is inspected before route/backend mutation so the event describes
// exactly what reached the gateway.
func logSafeMultimodalRequest(logger *slog.Logger, rawBody []byte) {
	if logger == nil || len(rawBody) == 0 || !bytes.Contains(rawBody, []byte(`"image_url"`)) {
		return
	}

	var envelope multimodalTraceEnvelope
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return
	}

	imageCount := 0
	for messageIndex, message := range envelope.Messages {
		if len(message.Content) == 0 || message.Content[0] != '[' {
			continue
		}
		var parts []multimodalTraceContentPart
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			continue
		}
		for partIndex, part := range parts {
			if part.Type != "image_url" || part.ImageURL == nil || part.ImageURL.URL == "" {
				continue
			}
			logSafeImageURL(logger, imageCount, messageIndex, partIndex, part.ImageURL.URL)
			imageCount++
		}
	}

	if imageCount > 0 {
		logger.Info("multimodal request detected",
			slog.Int("image_count", imageCount),
			slog.Int("request_body_bytes", len(rawBody)),
		)
	}
}

func logSafeImageURL(logger *slog.Logger, imageIndex, messageIndex, partIndex int, rawURL string) {
	hash := sha256.Sum256([]byte(rawURL))
	attrs := []slog.Attr{
		slog.Int("image_index", imageIndex),
		slog.Int("message_index", messageIndex),
		slog.Int("part_index", partIndex),
		slog.String("sha256", fmt.Sprintf("%x", hash[:8])),
	}

	if strings.HasPrefix(strings.ToLower(rawURL), "data:") {
		attrs = append(attrs, safeDataImageAttrs(rawURL)...)
	} else {
		scheme := "unknown"
		if parsed, err := url.Parse(rawURL); err == nil && parsed.Scheme != "" {
			scheme = strings.ToLower(parsed.Scheme)
		}
		attrs = append(attrs, slog.String("scheme", scheme))
	}

	logger.LogAttrs(context.Background(), slog.LevelInfo, "multimodal request image", attrs...)
}

func safeDataImageAttrs(rawURL string) []slog.Attr {
	attrs := []slog.Attr{slog.String("scheme", "data")}
	comma := strings.IndexByte(rawURL, ',')
	if comma < 0 {
		return append(attrs, slog.Bool("valid_data_uri", false))
	}

	metadata := rawURL[len("data:"):comma]
	payload := rawURL[comma+1:]
	metaParts := strings.Split(metadata, ";")
	mimeType := ""
	if len(metaParts) > 0 {
		mimeType = strings.ToLower(metaParts[0])
	}
	isBase64 := false
	for _, p := range metaParts[1:] {
		if strings.EqualFold(p, "base64") {
			isBase64 = true
			break
		}
	}

	attrs = append(attrs,
		slog.Bool("valid_data_uri", true),
		slog.String("mime", mimeType),
		slog.Int("encoded_len", len(payload)),
		slog.Bool("base64", isBase64),
	)
	if !isBase64 {
		return attrs
	}

	attrs = append(attrs, slog.Int("decoded_bytes_estimate", decodedBase64Size(payload)))
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
	cfg, format, err := image.DecodeConfig(io.LimitReader(reader, maxImageHeaderProbeBytes))
	if err == nil {
		attrs = append(attrs,
			slog.String("format", format),
			slog.Int("width", cfg.Width),
			slog.Int("height", cfg.Height),
			slog.Int64("pixels", int64(cfg.Width)*int64(cfg.Height)),
		)
	}
	return attrs
}

func decodedBase64Size(payload string) int {
	n := len(payload)
	if n == 0 {
		return 0
	}
	padding := 0
	if payload[n-1] == '=' {
		padding++
		if n > 1 && payload[n-2] == '=' {
			padding++
		}
	}
	decoded := base64.StdEncoding.DecodedLen(n) - padding
	if decoded < 0 {
		return 0
	}
	return decoded
}
