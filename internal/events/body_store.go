// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"context"
	"time"
)

const (
	externalStorageUploadPending = "upload_pending"
	externalStorageUploadFailed  = "upload_failed"
	externalStorageBodyTooLarge  = "body_exceeds_external_limit"
)

// BodyStore persists complete bodies that exceed the configured Kafka inline limit.
type BodyStore interface {
	MaxBodyBytes() int64
	Put(context.Context, BodyObject) (*BodyObjectReference, error)
}

// BodyObject is the complete object passed to a BodyStore.
type BodyObject struct {
	EventID   string
	RequestID string
	Kind      string
	Timestamp time.Time
	SHA256    string
	Body      []byte
}
