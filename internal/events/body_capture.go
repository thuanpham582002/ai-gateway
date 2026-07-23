// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"unicode/utf8"
)

type bodyCapture struct {
	maxBytes         int
	externalMaxBytes int64
	hasher           hash.Hash
	size             int64
	content          []byte
	externalContent  []byte
	externalOverflow bool
}

func newBodyCapture(maxBytes int) *bodyCapture {
	return newBodyCaptureWithExternalStore(maxBytes, 0)
}

func newBodyCaptureWithExternalStore(maxBytes int, externalMaxBytes int64) *bodyCapture {
	return &bodyCapture{maxBytes: maxBytes, externalMaxBytes: externalMaxBytes, hasher: sha256.New()}
}

func (b *bodyCapture) write(value []byte) {
	if b == nil {
		return
	}
	_, _ = b.hasher.Write(value)
	b.size += int64(len(value))
	if b.externalMaxBytes > 0 && !b.externalOverflow {
		if int64(len(b.externalContent))+int64(len(value)) > b.externalMaxBytes {
			b.externalContent = nil
			b.externalOverflow = true
		} else {
			b.externalContent = append(b.externalContent, value...)
		}
	}
	if b.maxBytes <= 0 || len(b.content) >= b.maxBytes {
		return
	}
	remaining := b.maxBytes - len(b.content)
	if remaining > len(value) {
		remaining = len(value)
	}
	b.content = append(b.content, value[:remaining]...)
}

func (b *bodyCapture) snapshot() *BodySnapshot {
	if b == nil {
		return nil
	}
	snapshot := &BodySnapshot{
		SHA256:    "sha256:" + hex.EncodeToString(b.hasher.Sum(nil)),
		SizeBytes: b.size,
		Truncated: b.size > int64(len(b.content)),
	}
	if len(b.content) == 0 {
		return snapshot
	}
	if utf8.Valid(b.content) {
		snapshot.Encoding = "utf-8"
		snapshot.Content = string(b.content)
	} else {
		snapshot.Encoding = "base64"
		snapshot.Content = base64.StdEncoding.EncodeToString(b.content)
	}
	return snapshot
}

func (b *bodyCapture) completeBody() ([]byte, bool) {
	if b == nil || b.externalMaxBytes <= 0 || b.externalOverflow || int64(len(b.externalContent)) != b.size {
		return nil, false
	}
	return b.externalContent, true
}
