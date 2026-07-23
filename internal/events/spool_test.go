// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/IBM/sarama/mocks"
	"github.com/stretchr/testify/require"
)

func TestSpoolRecordRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	value := []byte(`{"schema_version":1,"event_id":"event-1"}`)

	path, err := writeSpoolRecord(dir, "event-1", "request-1", value)
	require.NoError(t, err)
	require.FileExists(t, path)
	count, err := spoolRecordCount(dir)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	message, err := readSpoolRecord(path)
	require.NoError(t, err)
	key, err := message.Key.Encode()
	require.NoError(t, err)
	require.Equal(t, "request-1", string(key))
	actualValue, err := message.Value.Encode()
	require.NoError(t, err)
	require.JSONEq(t, string(value), string(actualValue))
	require.Equal(t, path, message.Metadata)
}

func TestPrepareSpoolDirRemovesInterruptedWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	temporaryPath := filepath.Join(dir, "event.json.tmp-interrupted")
	require.NoError(t, os.WriteFile(temporaryPath, []byte("partial"), 0o600))
	require.NoError(t, prepareSpoolDir(dir))
	require.NoFileExists(t, temporaryPath)
}

func TestKafkaFactoryReplaysAndRemovesAcknowledgedSpoolRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, err := writeSpoolRecord(dir, "event-1", "request-1", []byte(`{"schema_version":1}`))
	require.NoError(t, err)

	producer := mocks.NewAsyncProducer(t, newTestConfig())
	producer.ExpectInputAndSucceed()
	factory := &kafkaFactory{
		producer: producer,
		topic:    "events",
		spoolDir: dir,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	require.NoError(t, factory.replaySpooledEvents())
	message := <-producer.Successes()
	require.Equal(t, "events", message.Topic)
	require.Equal(t, path, message.Metadata)
	require.FileExists(t, path)

	factory.removeAcknowledgedSpoolRecord(message)
	require.NoFileExists(t, path)
}

func TestReadSpoolRecordRejectsInvalidData(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "invalid.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"key":"","value":null}`), 0o600))
	_, err := readSpoolRecord(path)
	require.ErrorContains(t, err, "invalid kafka spool record")
}
