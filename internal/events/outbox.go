// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

const outboxDatabaseName = "kafka-events.db"

var (
	outboxRecordsBucket    = []byte("records")
	outboxQuarantineBucket = []byte("quarantine")
	errOutboxFull          = errors.New("kafka event outbox is full")
)

type outboxRecord struct {
	EventID    string          `json:"event_id"`
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value"`
	Ready      bool            `json:"ready"`
	EnqueuedAt time.Time       `json:"enqueued_at"`
	SHA256     string          `json:"sha256"`
}

type outboxItem struct {
	sequence uint64
	record   outboxRecord
}

type durableOutbox struct {
	db         *bolt.DB
	maxBytes   int64
	maxRecords int64
	mutationMu sync.Mutex
	bytes      atomic.Int64
	records    atomic.Int64
}

func openDurableOutbox(dir string, maxBytes, maxRecords int64) (*durableOutbox, error) {
	if dir == "" {
		return nil, errors.New("kafka event outbox directory is required")
	}
	if maxBytes <= 0 || maxRecords <= 0 {
		return nil, errors.New("kafka event outbox limits must be positive")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create kafka event outbox directory: %w", err)
	}
	db, err := bolt.Open(filepath.Join(dir, outboxDatabaseName), 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open kafka event outbox: %w", err)
	}
	o := &durableOutbox{db: db, maxBytes: maxBytes, maxRecords: maxRecords}
	if err = o.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = o.migrateLegacySpool(dir); err != nil {
		_ = db.Close()
		return nil, err
	}
	return o, nil
}

func (o *durableOutbox) initialize() error {
	var totalBytes, totalRecords int64
	err := o.db.Update(func(tx *bolt.Tx) error {
		records, err := tx.CreateBucketIfNotExists(outboxRecordsBucket)
		if err != nil {
			return err
		}
		if _, err = tx.CreateBucketIfNotExists(outboxQuarantineBucket); err != nil {
			return err
		}
		type pendingMutation struct {
			key, value []byte
			quarantine bool
		}
		var mutations []pendingMutation
		if err = records.ForEach(func(key, value []byte) error {
			var record outboxRecord
			if decodeErr := json.Unmarshal(value, &record); decodeErr != nil || !record.valid() {
				mutations = append(mutations, pendingMutation{
					key: append([]byte(nil), key...), value: append([]byte(nil), value...), quarantine: true,
				})
				return nil
			}
			// A pending record means the process stopped during external body upload.
			// Publish the durable pending snapshot instead of permanently blocking it.
			if !record.Ready {
				record.Ready = true
				encoded, encodeErr := encodeOutboxRecord(record)
				if encodeErr != nil {
					return encodeErr
				}
				mutations = append(mutations, pendingMutation{
					key: append([]byte(nil), key...), value: encoded,
				})
				value = encoded
			}
			totalBytes += int64(len(value))
			totalRecords++
			return nil
		}); err != nil {
			return err
		}
		for _, mutation := range mutations {
			if mutation.quarantine {
				if err = o.quarantineInTransaction(tx, mutation.key, mutation.value); err != nil {
					return err
				}
			} else if err = records.Put(mutation.key, mutation.value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("initialize kafka event outbox: %w", err)
	}
	o.bytes.Store(totalBytes)
	o.records.Store(totalRecords)
	return nil
}

func (o *durableOutbox) enqueue(eventID, key string, value []byte, ready bool) (uint64, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	record := outboxRecord{EventID: eventID, Key: key, Value: append([]byte(nil), value...), Ready: ready, EnqueuedAt: time.Now().UTC()}
	encoded, err := encodeOutboxRecord(record)
	if err != nil {
		return 0, err
	}
	var sequence uint64
	err = o.db.Update(func(tx *bolt.Tx) error {
		if o.records.Load()+1 > o.maxRecords || o.bytes.Load()+int64(len(encoded)) > o.maxBytes {
			return errOutboxFull
		}
		bucket := tx.Bucket(outboxRecordsBucket)
		sequence, err = bucket.NextSequence()
		if err != nil {
			return err
		}
		return bucket.Put(sequenceKey(sequence), encoded)
	})
	if err != nil {
		return 0, err
	}
	o.bytes.Add(int64(len(encoded)))
	o.records.Add(1)
	return sequence, nil
}

func (o *durableOutbox) update(sequence uint64, value []byte, ready bool) error {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	var delta int64
	err := o.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(outboxRecordsBucket)
		key := sequenceKey(sequence)
		old := bucket.Get(key)
		if old == nil {
			return nil
		}
		var record outboxRecord
		if err := json.Unmarshal(old, &record); err != nil {
			return err
		}
		record.Value = append(record.Value[:0], value...)
		record.Ready = ready
		encoded, err := encodeOutboxRecord(record)
		if err != nil {
			return err
		}
		delta = int64(len(encoded) - len(old))
		if delta > 0 && o.bytes.Load()+delta > o.maxBytes {
			return errOutboxFull
		}
		if err = bucket.Put(key, encoded); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		o.bytes.Add(delta)
	}
	return err
}

func (o *durableOutbox) markReady(sequence uint64) error {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	var delta int64
	err := o.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(outboxRecordsBucket)
		key := sequenceKey(sequence)
		old := bucket.Get(key)
		if old == nil {
			return nil
		}
		var record outboxRecord
		if err := json.Unmarshal(old, &record); err != nil {
			return err
		}
		record.Ready = true
		encoded, err := encodeOutboxRecord(record)
		if err != nil {
			return err
		}
		if err = bucket.Put(key, encoded); err != nil {
			return err
		}
		delta = int64(len(encoded) - len(old))
		return nil
	})
	if err == nil {
		o.bytes.Add(delta)
	}
	return err
}

func (o *durableOutbox) peekReady() (*outboxItem, error) {
	var item *outboxItem
	err := o.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(outboxRecordsBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var record outboxRecord
			if err := json.Unmarshal(value, &record); err != nil || !record.valid() {
				return &corruptOutboxRecordError{sequence: binary.BigEndian.Uint64(key)}
			}
			if record.Ready {
				item = &outboxItem{sequence: binary.BigEndian.Uint64(key), record: record}
				return nil
			}
		}
		return nil
	})
	return item, err
}

func (o *durableOutbox) acknowledge(sequence uint64) error {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	var size int64
	err := o.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(outboxRecordsBucket)
		key := sequenceKey(sequence)
		value := bucket.Get(key)
		if value == nil {
			return nil
		}
		size = int64(len(value))
		if err := bucket.Delete(key); err != nil {
			return err
		}
		return nil
	})
	if err == nil && size > 0 {
		o.bytes.Add(-size)
		o.records.Add(-1)
	}
	return err
}

func (o *durableOutbox) quarantine(sequence uint64) error {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	var size int64
	err := o.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(outboxRecordsBucket)
		key := sequenceKey(sequence)
		value := bucket.Get(key)
		if value == nil {
			return nil
		}
		size = int64(len(value))
		return o.quarantineInTransaction(tx, key, value)
	})
	if err == nil && size > 0 {
		o.bytes.Add(-size)
		o.records.Add(-1)
	}
	return err
}

func (o *durableOutbox) quarantineInTransaction(tx *bolt.Tx, key, value []byte) error {
	copyValue := append([]byte(nil), value...)
	if err := tx.Bucket(outboxQuarantineBucket).Put(key, copyValue); err != nil {
		return err
	}
	if err := tx.Bucket(outboxRecordsBucket).Delete(key); err != nil {
		return err
	}
	return nil
}

func (o *durableOutbox) close() error { return o.db.Close() }

func (o *durableOutbox) stats() (records, bytes int64) { return o.records.Load(), o.bytes.Load() }

func (o *durableOutbox) oldestReadyAge(now time.Time) time.Duration {
	var enqueuedAt time.Time
	_ = o.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(outboxRecordsBucket).Cursor()
		for _, value := cursor.First(); value != nil; _, value = cursor.Next() {
			var record outboxRecord
			if json.Unmarshal(value, &record) == nil && record.Ready {
				enqueuedAt = record.EnqueuedAt
				break
			}
		}
		return nil
	})
	if enqueuedAt.IsZero() || now.Before(enqueuedAt) {
		return 0
	}
	return now.Sub(enqueuedAt)
}

func encodeOutboxRecord(record outboxRecord) ([]byte, error) {
	record.SHA256 = checksumRecord(record.Key, record.Value)
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode kafka event outbox record: %w", err)
	}
	return encoded, nil
}

func (r outboxRecord) valid() bool {
	return r.EventID != "" && r.Key != "" && json.Valid(r.Value) && r.SHA256 == checksumRecord(r.Key, r.Value)
}

func checksumRecord(key string, value []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(key))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(value)
	return hex.EncodeToString(hash.Sum(nil))
}

func sequenceKey(sequence uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, sequence)
	return key
}

type corruptOutboxRecordError struct{ sequence uint64 }

func (e *corruptOutboxRecordError) Error() string {
	return fmt.Sprintf("corrupt kafka event outbox record %d", e.sequence)
}

type legacySpoolRecord struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (o *durableOutbox) migrateLegacySpool(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		if !entry.IsDir() && strings.Contains(name, ".tmp-") {
			_ = os.Remove(path)
			continue
		}
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var legacy legacySpoolRecord
		if json.Unmarshal(raw, &legacy) != nil || legacy.Key == "" || !json.Valid(legacy.Value) {
			corruptPath := path + ".corrupt"
			if renameErr := os.Rename(path, corruptPath); renameErr != nil {
				return renameErr
			}
			continue
		}
		eventID := strings.TrimSuffix(name, filepath.Ext(name))
		if _, err = o.enqueue(eventID, legacy.Key, legacy.Value, true); err != nil {
			return fmt.Errorf("migrate legacy kafka spool record %s: %w", name, err)
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
