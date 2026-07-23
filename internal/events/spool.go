// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/IBM/sarama"
	"github.com/google/uuid"
)

type spoolRecord struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func prepareSpoolDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create kafka event spool directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read kafka event spool directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.Contains(entry.Name(), ".tmp-") {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

func writeSpoolRecord(dir, eventID, key string, value []byte) (string, error) {
	record, err := json.Marshal(spoolRecord{Key: key, Value: value})
	if err != nil {
		return "", fmt.Errorf("failed to marshal kafka spool record: %w", err)
	}
	finalPath := filepath.Join(dir, eventID+".json")
	temporaryPath := finalPath + ".tmp-" + uuid.NewString()
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err = file.Write(record); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(temporaryPath, finalPath); err != nil {
		return "", err
	}
	removeTemporary = false
	if dirHandle, openErr := os.Open(dir); openErr == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return finalPath, nil
}

func readSpoolRecord(path string) (*sarama.ProducerMessage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record spoolRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	if record.Key == "" || !json.Valid(record.Value) {
		return nil, fmt.Errorf("invalid kafka spool record")
	}
	return &sarama.ProducerMessage{
		Key:      sarama.StringEncoder(record.Key),
		Value:    sarama.ByteEncoder(record.Value),
		Metadata: path,
	}, nil
}

func (f *kafkaFactory) replaySpooledEvents() error {
	if f.spoolDir == "" {
		return nil
	}
	entries, err := os.ReadDir(f.spoolDir)
	if err != nil {
		return fmt.Errorf("failed to read kafka event spool: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(f.spoolDir, entry.Name())
		message, readErr := readSpoolRecord(path)
		if readErr != nil {
			f.logger.Error("failed to read kafka spool record", slog.Any("error", readErr), slog.String("path", path))
			continue
		}
		message.Topic = f.topic
		f.producer.Input() <- message
	}
	return nil
}

func (f *kafkaFactory) drainProducerResults() {
	errors := f.producer.Errors()
	successes := f.producer.Successes()
	for errors != nil || successes != nil {
		select {
		case producerError, ok := <-errors:
			if !ok {
				errors = nil
				continue
			}
			f.logger.Error("kafka producer error", slog.Any("error", producerError.Err))
		case message, ok := <-successes:
			if !ok {
				successes = nil
				continue
			}
			f.removeAcknowledgedSpoolRecord(message)
		}
	}
}

func (f *kafkaFactory) removeAcknowledgedSpoolRecord(message *sarama.ProducerMessage) {
	path, ok := message.Metadata.(string)
	if !ok || path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		f.logger.Error("failed to remove acknowledged kafka spool record", slog.Any("error", err), slog.String("path", path))
	}
}

func spoolRecordCount(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
		return nil
	})
	return count, err
}
