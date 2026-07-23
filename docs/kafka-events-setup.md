# Kafka Per-Request Event Publishing

AI Gateway ExtProc can emit per-request JSON events to Kafka for realtime dashboards, analytics, and monitoring.

## Prerequisites

- Kafka cluster accessible from the ExtProc pod
- Kafka topic created (default: `ai-gateway-events`)

## Quick Start

### 1. Install Kafka (Strimzi on Kubernetes)

```bash
# Create namespace
kubectl create namespace kafka

# Install Strimzi operator
kubectl create -f 'https://strimzi.io/install/latest?namespace=kafka' -n kafka
kubectl wait deployment/strimzi-cluster-operator -n kafka --for=condition=available --timeout=120s

# Create single-node Kafka cluster
cat <<'EOF' | kubectl apply -n kafka -f -
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaNodePool
metadata:
  name: combined
  labels:
    strimzi.io/cluster: ai-gateway-kafka
spec:
  replicas: 1
  roles: [controller, broker]
  storage:
    type: jbod
    volumes:
      - id: 0
        type: persistent-claim
        size: 10Gi
        deleteClaim: false
---
apiVersion: kafka.strimzi.io/v1beta2
kind: Kafka
metadata:
  name: ai-gateway-kafka
  annotations:
    strimzi.io/node-pools: enabled
    strimzi.io/kraft: enabled
spec:
  kafka:
    version: 4.1.0
    listeners:
      - name: plain
        port: 9092
        type: internal
        tls: false
    config:
      offsets.topic.replication.factor: 1
      transaction.state.log.replication.factor: 1
      transaction.state.log.min.isr: 1
      default.replication.factor: 1
      min.insync.replicas: 1
  entityOperator:
    topicOperator: {}
EOF

# Wait for Kafka to be ready
kubectl wait kafka/ai-gateway-kafka -n kafka --for=condition=Ready --timeout=300s
```

### 2. Create Topic

```bash
cat <<'EOF' | kubectl apply -n kafka -f -
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaTopic
metadata:
  name: ai-gateway-events
  labels:
    strimzi.io/cluster: ai-gateway-kafka
spec:
  partitions: 3
  replicas: 1
  config:
    retention.ms: 604800000   # 7 days
    cleanup.policy: delete
EOF
```

### 3. Configure AI Gateway

Deploy with Helm, passing the Kafka broker address via `extProc.extraEnvVars`:

```bash
helm upgrade aieg oci://docker.io/envoyproxy/ai-gateway-helm \
  --version v0.0.0-latest \
  --namespace envoy-ai-gateway-system \
  --set extProc.image.repository=ghcr.io/thuanpham582002/ai-gateway-extproc \
  --set extProc.image.tag=latest \
  --set extProc.imagePullPolicy=Always \
  --set 'extProc.extraEnvVars[0].name=KAFKA_BROKERS' \
  --set 'extProc.extraEnvVars[0].value=ai-gateway-kafka-kafka-bootstrap.kafka.svc.cluster.local:9092' \
  --force-conflicts
```

Then restart the controller and envoy pods to pick up the new config:

```bash
kubectl rollout restart deployment -n envoy-ai-gateway-system ai-gateway-controller
kubectl rollout restart deployment -n envoy-gateway-system envoy-model-serving-ai-gateway-ea0020c9
```

### 4. Verify

Check ExtProc logs for Kafka connection:

```bash
POD=$(kubectl get pods -n envoy-gateway-system -l app.kubernetes.io/name=envoy -o jsonpath='{.items[0].metadata.name}')
kubectl logs -n envoy-gateway-system $POD -c ai-gateway-extproc | grep kafka
# Expected: "kafka event publishing enabled" brokers=... topic=ai-gateway-events
```

Consume events from the topic:

```bash
kubectl run kafka-consumer -n kafka --rm -it --restart=Never \
  --image=quay.io/strimzi/kafka:latest-kafka-4.1.0 -- \
  bin/kafka-console-consumer.sh \
  --bootstrap-server ai-gateway-kafka-kafka-bootstrap:9092 \
  --topic ai-gateway-events \
  --from-beginning
```

## Configuration

### Environment Variables

Set via Helm `extProc.extraEnvVars`. These are read by ExtProc at startup as fallback when CLI flags are not set.

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `KAFKA_BROKERS` | `""` (disabled) | Comma-separated Kafka broker addresses. When empty, event publishing is disabled with zero overhead. |
| `KAFKA_TOPIC` | `ai-gateway-events` | Kafka topic name for events |
| `KAFKA_EVENT_HEADER_KEYS` | `""` | Comma-separated request header keys to include. Empty = include no raw headers. Native Codex and Claude correlation fields are extracted separately. |
| `KAFKA_EVENT_BODY_MAX_BYTES` | `0` | Maximum inline bytes captured for each inbound request, upstream request, and response body. Zero emits only SHA-256 and byte count. |
| `KAFKA_EVENT_SPOOL_DIR` | `""` | Optional directory for fsynced native Kafka events. Records are replayed after an ExtProc process restart and removed only after broker acknowledgement. |
| `KAFKA_EVENT_S3_BUCKET` | `""` (disabled) | S3-compatible bucket for complete bodies that exceed the inline limit. Supports AWS S3, SeaweedFS, MinIO, and compatible implementations. |
| `KAFKA_EVENT_S3_ENDPOINT` | AWS endpoint | Optional custom S3 endpoint URL, including `https://` or `http://`. |
| `KAFKA_EVENT_S3_REGION` | `us-east-1` | SigV4 signing region. |
| `KAFKA_EVENT_S3_PREFIX` | `ai-gateway-audit` | Object-key prefix. |
| `KAFKA_EVENT_S3_USE_PATH_STYLE` | `false` | Enable path-style addressing, normally required for SeaweedFS and MinIO. |
| `KAFKA_EVENT_S3_MAX_BODY_BYTES` | `16777216` | Maximum complete bytes retained per body for upload. Larger bodies remain hash-verifiable and report `body_exceeds_external_limit`. |
| `KAFKA_EVENT_S3_UPLOAD_TIMEOUT` | `15s` | Timeout for each object upload. |
| `KAFKA_EVENT_S3_CA_BUNDLE` | `""` | Path to an additional PEM CA bundle for private HTTPS endpoints. |
| `KAFKA_EVENT_S3_CA_PEM` | `""` | Additional CA certificate as PEM content, suitable for a Secret-backed environment variable. |
| `KAFKA_EVENT_S3_SERVER_SIDE_ENCRYPTION` | `""` | Optional `AES256` or `aws:kms` server-side encryption request. |
| `KAFKA_EVENT_S3_KMS_KEY_ID` | `""` | KMS key ID when server-side encryption is `aws:kms`. |
| `KAFKA_SASL_USER` | `""` | SASL username for authentication |
| `KAFKA_SASL_PASSWORD` | `""` | SASL password for authentication |
| `KAFKA_SASL_MECHANISM` | `PLAIN` | SASL mechanism (`PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`) |
| `KAFKA_TLS_ENABLED` | `false` | Enable TLS for Kafka connections |
| `KAFKA_TLS_CA_BUNDLE` | `""` | Path to an additional PEM CA bundle for Kafka brokers using a private CA. |
| `KAFKA_TLS_CA_PEM` | `""` | Additional Kafka CA certificate as Secret-backed PEM content. |

### CLI Flags

When running ExtProc directly (not via Helm), use CLI flags:

```bash
extproc \
  -kafkaBrokers=kafka:9092 \
  -kafkaTopic=ai-gateway-events \
  -kafkaEventHeaderKeys=x-project-id,x-model-id \
  -kafkaEventBodyMaxBytes=262144 \
  -kafkaEventSpoolDir=/var/lib/ai-gateway/events \
  -kafkaEventS3Bucket=ai-gateway-audit \
  -kafkaEventS3Endpoint=https://s3.seaweedfs.internal \
  -kafkaEventS3UsePathStyle=true \
  -kafkaEventS3CABundle=/etc/ai-gateway/s3-ca.pem \
  -kafkaSASLUser=user \
  -kafkaSASLPassword=pass \
  -kafkaSASLMechanism=SCRAM-SHA-256 \
  -kafkaTLSEnabled=true
  -kafkaTLSCABundle=/etc/kafka/ca.crt
```

### Header Filtering

Raw request headers are excluded by default. To include specific non-secret headers:

```bash
# Via env var
KAFKA_EVENT_HEADER_KEYS=x-project-id,x-model-id,user-agent

# Via CLI flag
-kafkaEventHeaderKeys=x-project-id,x-model-id,user-agent
```

Credential-valued headers such as authorization, authentication, API-key, token, signature, secret, and cookie headers are always suppressed, even if configured in the allowlist. Non-secret metadata such as `x-api-key-id` remains available for billing compatibility. Standard Codex and Claude Code lineage identifiers are projected into the structured `correlation` field even when raw header capture is disabled; identifiers longer than 1,024 bytes are omitted.

### Body Capture

Body hashes and byte counts are always emitted. Inline content is opt-in because prompts, tool calls, and model responses can contain sensitive data:

```bash
KAFKA_EVENT_BODY_MAX_BYTES=262144
```

Each body snapshot declares `encoding`, `content`, and `truncated`. UTF-8 content is stored directly; binary content is base64-encoded. The SHA-256 covers the complete body even when inline content is disabled or truncated. When ExtProc explicitly supplies an unchanged upstream body, `upstream_request_body.same_as` points to `request_body` instead of duplicating inline content.

Large bodies should not be retained in Kafka by only increasing this limit. Configure the S3-compatible overflow store to retain complete bodies outside Kafka:

```bash
KAFKA_EVENT_S3_BUCKET=ai-gateway-audit
KAFKA_EVENT_S3_ENDPOINT=https://s3.seaweedfs.internal
KAFKA_EVENT_S3_REGION=us-east-1
KAFKA_EVENT_S3_USE_PATH_STYLE=true
KAFKA_EVENT_S3_MAX_BODY_BYTES=16777216
KAFKA_EVENT_S3_CA_BUNDLE=/etc/ai-gateway/s3-ca.pem
AWS_ACCESS_KEY_ID=secret-backed-value
AWS_SECRET_ACCESS_KEY=secret-backed-value
```

The standard AWS credential chain is used, including environment credentials and workload identity. Custom endpoint certificates can be trusted by a mounted `KAFKA_EVENT_S3_CA_BUNDLE` file or Secret-backed `KAFKA_EVENT_S3_CA_PEM` content; system roots remain trusted. TLS verification cannot be disabled.

Only bodies whose inline content is truncated are uploaded. Object keys have the form `<prefix>/<UTC date>/<event_id>/<body kind>.bin`. Kafka retains the full-body SHA-256, byte count, bounded inline prefix, and an `object` reference containing the S3 bucket and key. An upload failure does not suppress the audit event: `external_storage_error` reports `upload_failed`, while a body over the external bound reports `body_exceeds_external_limit`. With the native Kafka spool enabled, `upload_pending` is fsynced before upload begins and atomically replaced by the final event; it is replayed only when the process crashes before the object result is known.

The S3 client uses SigV4 and path-style endpoints when configured, making the same implementation suitable for SeaweedFS in managed environments and MinIO in development. Optional `AES256` and `aws:kms` headers are passed through only when supported by the target service.

### Crash Replay

`KAFKA_EVENT_SPOOL_DIR` enables an atomic, fsynced spool for the native Kafka producer. The producer writes the event before enqueueing it, retains it when retries are exhausted, replays it at startup with the same `event_id`, and removes it only after Kafka acknowledgement. A crash after broker acknowledgement but before local deletion can produce a duplicate; consumers should deduplicate on `event_id`.

The live deployment uses the ExtProc UDS `emptyDir`, which survives a container restart but not pod deletion or node loss. Use a per-pod persistent volume or an external outbox when those failures must also be durable. Kafka REST publishing does not support the local spool.

## Event Schema

Each request produces one JSON event:

```json
{
  "schema_version": 1,
  "event_id": "26593302-bd73-4f2c-8fc1-b1dd3fc5ca7d",
  "run_id": "ef41f8b6-7691-479c-8f0c-a9f41128440f",
  "event_type": "request_completed",
  "timestamp": "2026-03-23T11:42:57.502Z",
  "request_id": "13bc9e5e-9e51-4093-9912-03dc0fdc8506",
  "operation": "completion",
  "protocol": "openai.completions",
  "transport": "http_sse",
  "conversation_key": "session:session-1/thread:thread-2",
  "correlation": {
    "session_id": "session-1",
    "thread_id": "thread-2",
    "provider_response_id": "resp-123"
  },
  "original_model": "ep-e0dbf27b-v1",
  "request_model": "ep-e0dbf27b-v1",
  "response_model": "ep-e0dbf27b-v1",
  "backend": "OpenAI",
  "backend_name": "testproject/pool/route/my-route/rule/0/ref/0",
  "success": true,
  "error_type": "",
  "latency_ms": 1013,
  "stream": true,
  "time_to_first_token_ms": 205,
  "inter_token_latency_ms": 12.3,
  "tokens": {
    "input_tokens": 150,
    "output_tokens": 250,
    "total_tokens": 400,
    "cached_input_tokens": 50,
    "cache_creation_input_tokens": 0
  },
  "billing": {
    "api_key_id": "key-123",
    "billing_request_id": "billing-456",
    "reserved_input_tokens": 512
  },
  "capture_policy": {
    "mode": "bounded_inline",
    "max_inline_bytes": 4096,
    "external_body_store": {
      "provider": "s3",
      "max_body_bytes": 16777216
    }
  },
  "selected_pool": "vllm-pool-v2",
  "model_name_override": "Qwen/Qwen3-0.6B",
  "headers": {
    "x-project-id": "project-123",
    "x-model-id": "model-456",
    "user-agent": "curl/8.16.0"
  },
  "request_body": {
    "sha256": "sha256:...",
    "size_bytes": 8418,
    "encoding": "utf-8",
    "content": "{\"model\":\"ep-e0dbf27b-v1\",...}",
    "truncated": true,
    "object": {
      "provider": "s3",
      "bucket": "ai-gateway-audit",
      "key": "request-bodies/2026/07/23/26593302-bd73-4f2c-8fc1-b1dd3fc5ca7d/request.bin"
    }
  }
}
```

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `schema_version` | integer | Version of the additive event envelope |
| `event_id` | string | Unique ID for this emitted event |
| `run_id` | string | Gateway-generated model-run ID |
| `event_type` | string | `"request_completed"` or `"request_failed"` |
| `timestamp` | string | ISO 8601 timestamp of event emission |
| `request_id` | string | Envoy `x-request-id` header value, or a generated fallback when absent |
| `operation` | string | `chat`, `completion`, `embeddings`, `messages`, `responses`, `responses_compact`, `speech`, `image_generation`, `rerank` |
| `protocol` | string | Native request protocol, such as `openai.responses` or `anthropic.messages` |
| `transport` | string | `http` or `http_sse` |
| `client` | object | Best-effort client identification; never used for routing |
| `conversation_key` | string | Stable native conversation/session/thread key when the protocol provides enough identity; omitted rather than inferred from message history |
| `correlation` | object | Native session, thread, turn, response, or agent lineage. `provider_response_id` is populated for Chat Completions, Responses, and Messages; Responses also preserves `response_id` for compatibility. |
| `original_model` | string | Model name from user's request body |
| `request_model` | string | Model after override/mapping applied |
| `response_model` | string | Model reported by backend in response |
| `backend` | string | Provider type: `OpenAI`, `Anthropic`, `Cohere`, etc. |
| `backend_name` | string | Full backend ref identifier: `namespace/name/route/routeName/rule/index/ref/backendIndex` |
| `success` | bool | Whether request completed successfully |
| `error_type` | string | Error category (omitted on success): `invalid_request`, `backend_error`, `transform_error`, `auth_error`, `config_error`, `internal_error` |
| `latency_ms` | float | Total request duration in milliseconds |
| `stream` | bool | Whether response was streamed |
| `time_to_first_token_ms` | float | Time to first token (streaming only) |
| `inter_token_latency_ms` | float | Average inter-token latency (streaming only) |
| `tokens` | object | Token usage counts (null if unavailable) |
| `billing` | object | Typed billing identifiers, exact decimal prices/costs, booleans, reservations, and concurrency slots projected from processed headers |
| `capture_policy` | object | Effective inline policy and optional S3-compatible external-body limit |
| `selected_pool` | string | InferencePool selected by weighted/session affinity routing |
| `model_name_override` | string | Actual model name sent to backend when override is configured |
| `headers` | object | Request headers explicitly selected by configuration |
| `request_body` | object | Hash, size, optional bounded content, and optional complete-body S3 object reference for the native inbound request |
| `upstream_request_body` | object | Hash, size, and optional bounded content when ExtProc explicitly supplies the backend body; omitted when another filter may own the final bytes |
| `response_body` | object | Hash, size, and optional bounded content accumulated from the final response |

### Kafka Message Key

Each message uses `request_id` as the Kafka message key, ensuring all events for the same request land on the same partition.

The existing key is intentionally preserved for billing compatibility. Multi-turn consumers should use `conversation_key`, correlation edges, and timestamps rather than assuming turns share a partition.

### Response Identity Headers

ExtProc adds `x-request-id` and `x-ai-gateway-run-id` to response headers. This exposes the exact Kafka message key and model-run identity without requiring clients to send custom headers.

## Deployment And Acceptance

The checked-in live values are in `deploy/ai-infer-factory/ai-gateway-values.yaml`. For the development MinIO fixture, create the bucket-scoped user, seven-day lifecycle, Kubernetes credential Secret, and shared GatewayConfig first:

```bash
scripts/configure-dev-minio-audit.sh
```

The credential values are generated at runtime and stored only in the `envoy-gateway-system/ai-gateway-audit-s3` Secret. The checked-in `deploy/ai-infer-factory/audit-s3-gateway-config.yaml` projects them into ExtProc without placing them in Helm values.

The deploy gate builds an immutable ExtProc image, upgrades the Helm release atomically, waits for all data-plane rollouts, verifies injected images, consumes the resulting Kafka event, and rolls the Helm release back when acceptance fails:

```bash
GATEWAY_URL=http://10.24.10.200/v1/projects/cmp/locations/lab-24/endpoints/seed-md-gateway-acceptance/v1/chat/completions \
MODEL=seed-served-model \
EXPECTED_RESPONSE_SUBSTRING=gateway-pong \
scripts/deploy-kafka-audit-acceptance.sh
```

Set `EXPECT_S3_BODY_OBJECT=true` to send a body larger than the inline limit and require an S3 object reference in the accepted Kafka event.

Run only the Kafka acceptance test against an existing deployment with `scripts/kafka-audit-acceptance.sh` and the same environment variables.

The formal event contract is [`kafka-request-event.schema.json`](./kafka-request-event.schema.json).

## Disabling

To disable event publishing, simply don't set `KAFKA_BROKERS`. When no broker is configured, a no-op publisher is used with zero overhead.
