// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mainlib

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_parseAndValidateFlags(t *testing.T) {
	t.Setenv("KAFKA_EVENT_BODY_MAX_BYTES", "")
	t.Setenv("KAFKA_EVENT_SPOOL_DIR", "")
	t.Run("ok extProcFlags", func(t *testing.T) {
		for _, tc := range []struct {
			name                   string
			args                   []string
			configPath             string
			addr                   string
			rootPrefix             string
			logLevel               slog.Level
			enableRedaction        bool
			kafkaEventBodyMaxBytes int
			kafkaEventSpoolDir     string
		}{
			{
				name:            "minimal extProcFlags",
				args:            []string{"-configPath", "/path/to/config.yaml"},
				configPath:      "/path/to/config.yaml",
				addr:            ":1063",
				rootPrefix:      "/",
				logLevel:        slog.LevelInfo,
				enableRedaction: false,
			},
			{
				name:            "custom addr",
				args:            []string{"-configPath", "/path/to/config.yaml", "-extProcAddr", "unix:///tmp/ext_proc.sock"},
				configPath:      "/path/to/config.yaml",
				addr:            "unix:///tmp/ext_proc.sock",
				rootPrefix:      "/",
				logLevel:        slog.LevelInfo,
				enableRedaction: false,
			},
			{
				name:            "log level debug",
				args:            []string{"-configPath", "/path/to/config.yaml", "-logLevel", "debug"},
				configPath:      "/path/to/config.yaml",
				addr:            ":1063",
				rootPrefix:      "/",
				logLevel:        slog.LevelDebug,
				enableRedaction: false,
			},
			{
				name:            "log level debug with redaction enabled",
				args:            []string{"-configPath", "/path/to/config.yaml", "-logLevel", "debug", "-enableRedaction"},
				configPath:      "/path/to/config.yaml",
				addr:            ":1063",
				rootPrefix:      "/",
				logLevel:        slog.LevelDebug,
				enableRedaction: true,
			},
			{
				name:            "log level warn",
				args:            []string{"-configPath", "/path/to/config.yaml", "-logLevel", "warn"},
				configPath:      "/path/to/config.yaml",
				addr:            ":1063",
				rootPrefix:      "/",
				logLevel:        slog.LevelWarn,
				enableRedaction: false,
			},
			{
				name:            "log level error",
				args:            []string{"-configPath", "/path/to/config.yaml", "-logLevel", "error"},
				configPath:      "/path/to/config.yaml",
				addr:            ":1063",
				rootPrefix:      "/",
				logLevel:        slog.LevelError,
				enableRedaction: false,
			},
			{
				name: "all extProcFlags",
				args: []string{
					"-configPath", "/path/to/config.yaml",
					"-extProcAddr", "unix:///tmp/ext_proc.sock",
					"-logLevel", "debug",
					"-rootPrefix", "/foo/bar/",
				},
				configPath:      "/path/to/config.yaml",
				addr:            "unix:///tmp/ext_proc.sock",
				rootPrefix:      "/foo/bar/",
				logLevel:        slog.LevelDebug,
				enableRedaction: false,
			},
			{
				name:            "with endpoint prefixes",
				args:            []string{"-configPath", "/path/to/config.yaml", "-endpointPrefixes", "openai:/,cohere:/cohere,anthropic:/anthropic"},
				configPath:      "/path/to/config.yaml",
				addr:            ":1063",
				rootPrefix:      "/",
				logLevel:        slog.LevelInfo,
				enableRedaction: false,
			},
			{
				name:                   "with bounded Kafka body capture",
				args:                   []string{"-configPath", "/path/to/config.yaml", "-kafkaEventBodyMaxBytes", "262144", "-kafkaEventSpoolDir", "/var/lib/aigw/events"},
				configPath:             "/path/to/config.yaml",
				addr:                   ":1063",
				rootPrefix:             "/",
				logLevel:               slog.LevelInfo,
				enableRedaction:        false,
				kafkaEventBodyMaxBytes: 262144,
				kafkaEventSpoolDir:     "/var/lib/aigw/events",
			},
			{
				name: "with metrics header mapping",
				args: []string{
					"-configPath", "/path/to/config.yaml",
					"-metricsRequestHeaderAttributes", "x-tenant-id:tenant.id,x-tenant-id:tenant.id",
				},
				configPath:      "/path/to/config.yaml",
				rootPrefix:      "/",
				addr:            ":1063",
				logLevel:        slog.LevelInfo,
				enableRedaction: false,
			},
			{
				name: "with base header mapping",
				args: []string{
					"-configPath", "/path/to/config.yaml",
					"-metricsRequestHeaderAttributes", "x-team-id:team.id,x-user-id:user.id",
				},
				configPath:      "/path/to/config.yaml",
				rootPrefix:      "/",
				addr:            ":1063",
				logLevel:        slog.LevelInfo,
				enableRedaction: false,
			},
			{
				name: "with tracing header attributes",
				args: []string{
					"-configPath", "/path/to/config.yaml",
					"-spanRequestHeaderAttributes", "x-session-id:session.id,x-user-id:user.id",
				},
				configPath:      "/path/to/config.yaml",
				rootPrefix:      "/",
				addr:            ":1063",
				logLevel:        slog.LevelInfo,
				enableRedaction: false,
			},
			{
				name: "with both metrics and tracing headers",
				args: []string{
					"-configPath", "/path/to/config.yaml",
					"-metricsRequestHeaderAttributes", "x-user-id:user.id",
					"-spanRequestHeaderAttributes", "x-session-id:session.id",
				},
				configPath:      "/path/to/config.yaml",
				rootPrefix:      "/",
				addr:            ":1063",
				logLevel:        slog.LevelInfo,
				enableRedaction: false,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				flags, err := parseAndValidateFlags(tc.args)
				require.NoError(t, err)
				require.Equal(t, tc.configPath, flags.configPath)
				require.Equal(t, tc.addr, flags.extProcAddr)
				require.Equal(t, tc.logLevel, flags.logLevel)
				require.Equal(t, tc.enableRedaction, flags.enableRedaction)
				require.Equal(t, tc.rootPrefix, flags.rootPrefix)
				require.Equal(t, tc.kafkaEventBodyMaxBytes, flags.kafkaEventBodyMaxBytes)
				require.Equal(t, tc.kafkaEventSpoolDir, flags.kafkaEventSpoolDir)
			})
		}
	})

	t.Run("invalid extProcFlags", func(t *testing.T) {
		tests := []struct {
			name          string
			args          []string
			expectedError string
		}{
			{
				name:          "invalid log level",
				args:          []string{"-logLevel", "invalid"},
				expectedError: "configPath must be provided\nfailed to unmarshal log level: slog: level string \"invalid\": unknown name",
			},
			{
				name:          "invalid endpoint prefixes - unknown key",
				args:          []string{"-configPath", "/path/to/config.yaml", "-endpointPrefixes", "foo:/x"},
				expectedError: "failed to parse endpoint prefixes: unknown endpointPrefixes key \"foo\" at position 1 (allowed: openai, cohere, anthropic)",
			},
			{
				name:          "invalid endpoint prefixes - missing colon",
				args:          []string{"-configPath", "/path/to/config.yaml", "-endpointPrefixes", "openai"},
				expectedError: "failed to parse endpoint prefixes: invalid endpointPrefixes pair at position 1: \"openai\" (expected format: key:value)",
			},
			{
				name:          "invalid tracing header attributes - missing colon",
				args:          []string{"-configPath", "/path/to/config.yaml", "-spanRequestHeaderAttributes", "x-session-id"},
				expectedError: "failed to parse tracing header mapping: invalid header-attribute pair at position 1: \"x-session-id\" (expected format: header:attribute)",
			},
			{
				name:          "invalid tracing header attributes - empty header",
				args:          []string{"-configPath", "/path/to/config.yaml", "-spanRequestHeaderAttributes", ":session.id"},
				expectedError: "failed to parse tracing header mapping: empty header or attribute at position 1: \":session.id\"",
			},
			{
				name:          "negative Kafka body capture",
				args:          []string{"-configPath", "/path/to/config.yaml", "-kafkaEventBodyMaxBytes", "-1"},
				expectedError: "kafkaEventBodyMaxBytes must be non-negative",
			},
			{
				name:          "spool with Kafka REST",
				args:          []string{"-configPath", "/path/to/config.yaml", "-kafkaRESTURL", "http://kafka-rest", "-kafkaEventSpoolDir", "/events"},
				expectedError: "kafkaEventSpoolDir is supported only by the native Kafka producer",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := parseAndValidateFlags(tt.args)
				require.EqualError(t, err, tt.expectedError)
			})
		}
	})
}

func TestParseAndValidateFlagsKafkaBodyCaptureFromEnv(t *testing.T) {
	t.Setenv("KAFKA_EVENT_BODY_MAX_BYTES", "4096")
	t.Setenv("KAFKA_EVENT_SPOOL_DIR", "/var/lib/aigw/events")
	flags, err := parseAndValidateFlags([]string{"-configPath", "/path/to/config.yaml"})
	require.NoError(t, err)
	require.Equal(t, 4096, flags.kafkaEventBodyMaxBytes)
	require.Equal(t, "/var/lib/aigw/events", flags.kafkaEventSpoolDir)
}

func TestParseAndValidateFlagsKafkaCustomCAFromEnv(t *testing.T) {
	t.Setenv("KAFKA_TLS_ENABLED", "true")
	t.Setenv("KAFKA_TLS_CA_BUNDLE", "/etc/kafka/ca.crt")
	t.Setenv("KAFKA_TLS_CA_PEM", "certificate")

	flags, err := parseAndValidateFlags([]string{"-configPath", "/path/to/config.yaml"})
	require.NoError(t, err)
	require.True(t, flags.kafkaTLSEnabled)
	require.Equal(t, "/etc/kafka/ca.crt", flags.kafkaTLSCABundle)
	require.Equal(t, "certificate", flags.kafkaTLSCAPEM)
}

func TestParseAndValidateFlagsRejectsKafkaCAWithoutTLS(t *testing.T) {
	t.Setenv("KAFKA_TLS_ENABLED", "false")
	t.Setenv("KAFKA_TLS_CA_PEM", "certificate")

	_, err := parseAndValidateFlags([]string{"-configPath", "/path/to/config.yaml"})
	require.EqualError(t, err, "Kafka CA configuration requires Kafka TLS")
}

func TestParseAndValidateFlagsS3BodyStoreFromEnv(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "kafka:9092")
	t.Setenv("KAFKA_EVENT_S3_BUCKET", "audit-bucket")
	t.Setenv("KAFKA_EVENT_S3_ENDPOINT", "https://seaweedfs.example.test")
	t.Setenv("KAFKA_EVENT_S3_REGION", "local")
	t.Setenv("KAFKA_EVENT_S3_PREFIX", "events")
	t.Setenv("KAFKA_EVENT_S3_CA_BUNDLE", "/etc/s3/ca.pem")
	t.Setenv("KAFKA_EVENT_S3_CA_PEM", "certificate")
	t.Setenv("KAFKA_EVENT_S3_USE_PATH_STYLE", "true")
	t.Setenv("KAFKA_EVENT_S3_MAX_BODY_BYTES", "1048576")
	t.Setenv("KAFKA_EVENT_S3_UPLOAD_TIMEOUT", "30s")
	t.Setenv("KAFKA_EVENT_S3_SERVER_SIDE_ENCRYPTION", "aws:kms")
	t.Setenv("KAFKA_EVENT_S3_KMS_KEY_ID", "key-1")

	flags, err := parseAndValidateFlags([]string{"-configPath", "/path/to/config.yaml"})
	require.NoError(t, err)
	require.Equal(t, "audit-bucket", flags.kafkaEventS3Bucket)
	require.Equal(t, "https://seaweedfs.example.test", flags.kafkaEventS3Endpoint)
	require.Equal(t, "local", flags.kafkaEventS3Region)
	require.Equal(t, "events", flags.kafkaEventS3Prefix)
	require.Equal(t, "/etc/s3/ca.pem", flags.kafkaEventS3CABundle)
	require.Equal(t, "certificate", flags.kafkaEventS3CAPEM)
	require.True(t, flags.kafkaEventS3UsePathStyle)
	require.Equal(t, int64(1048576), flags.kafkaEventS3MaxBodyBytes)
	require.Equal(t, 30*time.Second, flags.kafkaEventS3UploadTimeout)
	require.Equal(t, "aws:kms", flags.kafkaEventS3ServerSideEncryption)
	require.Equal(t, "key-1", flags.kafkaEventS3KMSKeyID)
}

func TestParseAndValidateFlagsRejectsInvalidS3BodyStoreEnv(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "kafka:9092")
	t.Setenv("KAFKA_EVENT_S3_BUCKET", "audit-bucket")
	t.Setenv("KAFKA_EVENT_S3_USE_PATH_STYLE", "invalid")
	t.Setenv("KAFKA_EVENT_S3_MAX_BODY_BYTES", "0")
	t.Setenv("KAFKA_EVENT_S3_UPLOAD_TIMEOUT", "invalid")

	_, err := parseAndValidateFlags([]string{"-configPath", "/path/to/config.yaml"})
	require.ErrorContains(t, err, "KAFKA_EVENT_S3_USE_PATH_STYLE must be a boolean")
	require.ErrorContains(t, err, "kafkaEventS3MaxBodyBytes must be positive")
	require.ErrorContains(t, err, "KAFKA_EVENT_S3_UPLOAD_TIMEOUT must be a duration")
}

func TestListenAddress(t *testing.T) {
	unixPath := t.TempDir() + "/extproc.sock"
	// Create a stale file to ensure that removing the file works correctly.
	require.NoError(t, os.WriteFile(unixPath, []byte("stale socket"), 0o600))

	lis, err := listen(t.Context(), t.Name(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer lis.Close() //nolint:errcheck

	tests := []struct {
		addr        string
		wantNetwork string
		wantAddress string
	}{
		{lis.Addr().String(), "tcp", lis.Addr().String()},
		{"unix://" + unixPath, "unix", unixPath},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			network, address := listenAddress(tt.addr)
			require.Equal(t, tt.wantNetwork, network)
			require.Equal(t, tt.wantAddress, address)
		})
	}
	_, err = os.Stat(unixPath)
	require.ErrorIs(t, err, os.ErrNotExist, "expected the stale socket file to be removed")
}

// TestExtProcStartupMessage ensures other programs can rely on the startup message to STDERR.
func TestExtProcStartupMessage(t *testing.T) {
	// Create a temporary config file.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
version: dev
backends:
- name: openai
  schema:
    name: OpenAI
    version: v1
`), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Create a pipe for stderr.
	stderrR, stderrW := io.Pipe()

	// Start a goroutine to scan stderr until it reaches "AI Gateway External Processor is ready" written by envoy.
	go func() {
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "AI Gateway External Processor is ready") {
				cancel() // interrupts extproc.
				return
			}
		}
	}()

	// UNIX doesn't like the long socket paths, so create a temp directory for the socket instead of t.TempDir.
	socketTempDir := "/tmp/" + uuid.NewString()
	t.Cleanup(func() { _ = os.RemoveAll(socketTempDir) })
	require.NoError(t, os.MkdirAll(socketTempDir, 0o700))
	socketPath := filepath.Join(socketTempDir, "mcp.sock")

	// Run ExtProc in a goroutine on ephemeral ports.
	errCh := make(chan error, 1)
	go func() {
		args := []string{
			"-configPath", configPath,
			"-extProcAddr", ":0",
			"-adminPort", "0",
			"-mcpAddr", "unix://" + socketPath,
			"-logLevel", "info",
		}
		errCh <- Main(ctx, args, stderrW)
	}()

	timeout, cancelTimeout := context.WithTimeout(t.Context(), time.Second*3)
	defer cancelTimeout()
	select {
	case <-ctx.Done():
	case <-timeout.Done():
		t.Fatal("timeout waiting for startup message")
	case err := <-errCh:
		require.NoError(t, err, "extproc exited with error before startup message")
	}
}
