// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func openTestOutbox(t *testing.T, dir string, maxBytes, maxRecords int64) *durableOutbox {
	t.Helper()
	outbox, err := openDurableOutbox(dir, maxBytes, maxRecords)
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.close() })
	return outbox
}

func TestDurableOutboxPersistsFIFOAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	outbox, err := openDurableOutbox(dir, 1<<20, 100)
	require.NoError(t, err)
	first, err := outbox.enqueue("event-1", "request-1", []byte(`{"event_id":"event-1"}`), true)
	require.NoError(t, err)
	second, err := outbox.enqueue("event-2", "request-2", []byte(`{"event_id":"event-2"}`), true)
	require.NoError(t, err)
	require.Less(t, first, second)
	require.NoError(t, outbox.close())

	outbox, err = openDurableOutbox(dir, 1<<20, 100)
	require.NoError(t, err)
	defer outbox.close()
	item, err := outbox.peekReady()
	require.NoError(t, err)
	require.Equal(t, first, item.sequence)
	require.Equal(t, "request-1", item.record.Key)
	require.NoError(t, outbox.acknowledge(first))
	item, err = outbox.peekReady()
	require.NoError(t, err)
	require.Equal(t, second, item.sequence)
	require.Equal(t, int64(1), outbox.records.Load())
}

func TestDurableOutboxPromotesPendingRecordAfterRestart(t *testing.T) {
	dir := t.TempDir()
	outbox, err := openDurableOutbox(dir, 1<<20, 100)
	require.NoError(t, err)
	_, err = outbox.enqueue("event-1", "request-1", []byte(`{"event_id":"event-1","request_body":{"external_storage_error":"upload_pending"}}`), false)
	require.NoError(t, err)
	item, err := outbox.peekReady()
	require.NoError(t, err)
	require.Nil(t, item)
	require.NoError(t, outbox.close())

	outbox, err = openDurableOutbox(dir, 1<<20, 100)
	require.NoError(t, err)
	defer outbox.close()
	item, err = outbox.peekReady()
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Contains(t, string(item.record.Value), externalStorageUploadPending)
}

func TestDurableOutboxEnforcesRecordAndByteLimits(t *testing.T) {
	t.Run("records", func(t *testing.T) {
		outbox := openTestOutbox(t, t.TempDir(), 1<<20, 1)
		_, err := outbox.enqueue("event-1", "request-1", []byte(`{"event_id":"event-1"}`), true)
		require.NoError(t, err)
		_, err = outbox.enqueue("event-2", "request-2", []byte(`{"event_id":"event-2"}`), true)
		require.ErrorIs(t, err, errOutboxFull)
	})

	t.Run("bytes", func(t *testing.T) {
		outbox := openTestOutbox(t, t.TempDir(), 1, 10)
		_, err := outbox.enqueue("event-1", "request-1", []byte(`{"event_id":"event-1"}`), true)
		require.ErrorIs(t, err, errOutboxFull)
	})
}

func TestDurableOutboxConcurrentCapacityInvariant(t *testing.T) {
	outbox := openTestOutbox(t, t.TempDir(), 1<<20, 8)
	var wg sync.WaitGroup
	for index := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "event-" + time.Unix(int64(index), 0).Format("150405")
			_, err := outbox.enqueue(id, id, []byte(`{"ok":true}`), true)
			if err != nil && !errors.Is(err, errOutboxFull) {
				t.Errorf("enqueue: %v", err)
			}
		}()
	}
	wg.Wait()
	records, _ := outbox.stats()
	require.Equal(t, int64(8), records)
}

func TestDurableOutboxQuarantinesCorruptRecord(t *testing.T) {
	outbox := openTestOutbox(t, t.TempDir(), 1<<20, 100)
	sequence, err := outbox.enqueue("event-1", "request-1", []byte(`{"event_id":"event-1"}`), true)
	require.NoError(t, err)
	require.NoError(t, outbox.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(outboxRecordsBucket).Put(sequenceKey(sequence), []byte(`{"broken":true}`))
	}))
	_, err = outbox.peekReady()
	var corrupt *corruptOutboxRecordError
	require.ErrorAs(t, err, &corrupt)
	require.NoError(t, outbox.quarantine(corrupt.sequence))
	require.NoError(t, outbox.db.View(func(tx *bolt.Tx) error {
		require.Nil(t, tx.Bucket(outboxRecordsBucket).Get(sequenceKey(sequence)))
		require.NotNil(t, tx.Bucket(outboxQuarantineBucket).Get(sequenceKey(sequence)))
		return nil
	}))
}

func TestDurableOutboxMigratesLegacyJSONSpool(t *testing.T) {
	dir := t.TempDir()
	legacy := legacySpoolRecord{Key: "request-1", Value: json.RawMessage(`{"event_id":"event-1"}`)}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	legacyPath := filepath.Join(dir, "event-1.json")
	require.NoError(t, os.WriteFile(legacyPath, raw, 0o600))
	outbox := openTestOutbox(t, dir, 1<<20, 100)
	require.NoFileExists(t, legacyPath)
	item, err := outbox.peekReady()
	require.NoError(t, err)
	require.Equal(t, "request-1", item.record.Key)
	require.JSONEq(t, `{"event_id":"event-1"}`, string(item.record.Value))
}
