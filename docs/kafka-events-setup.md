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
| `KAFKA_EVENT_HEADER_KEYS` | `""` (all headers) | Comma-separated request header keys to include. Empty = include all headers. |
| `KAFKA_SASL_USER` | `""` | SASL username for authentication |
| `KAFKA_SASL_PASSWORD` | `""` | SASL password for authentication |
| `KAFKA_SASL_MECHANISM` | `PLAIN` | SASL mechanism (`PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`) |
| `KAFKA_TLS_ENABLED` | `false` | Enable TLS for Kafka connections |

### CLI Flags

When running ExtProc directly (not via Helm), use CLI flags:

```bash
extproc \
  -kafkaBrokers=kafka:9092 \
  -kafkaTopic=ai-gateway-events \
  -kafkaEventHeaderKeys=x-session-id,x-api-key \
  -kafkaSASLUser=user \
  -kafkaSASLPassword=pass \
  -kafkaSASLMechanism=SCRAM-SHA-256 \
  -kafkaTLSEnabled=true
```

### Header Filtering

By default, **all request headers** are included in events. To include only specific headers:

```bash
# Via env var
KAFKA_EVENT_HEADER_KEYS=x-session-id,x-api-key,user-agent

# Via CLI flag
-kafkaEventHeaderKeys=x-session-id,x-api-key,user-agent
```

## Event Schema

Each request produces one JSON event:

```json
{
  "event_type": "request_completed",
  "timestamp": "2026-03-23T11:42:57.502Z",
  "request_id": "13bc9e5e-9e51-4093-9912-03dc0fdc8506",
  "operation": "completion",
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
  "selected_pool": "vllm-pool-v2",
  "model_name_override": "Qwen/Qwen3-0.6B",
  "headers": {
    ":authority": "ai-gateway.local",
    ":method": "POST",
    ":path": "/v1/completions",
    "x-session-id": "test-session-123",
    "x-request-id": "13bc9e5e-...",
    "x-ai-eg-model": "ep-e0dbf27b-v1",
    "user-agent": "curl/8.16.0"
  }
}
```

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `event_type` | string | `"request_completed"` or `"request_failed"` |
| `timestamp` | string | ISO 8601 timestamp of event emission |
| `request_id` | string | Envoy `x-request-id` header value |
| `operation` | string | `chat`, `completion`, `embeddings`, `messages`, `responses`, `speech`, `image_generation`, `rerank` |
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
| `selected_pool` | string | InferencePool selected by weighted/session affinity routing |
| `model_name_override` | string | Actual model name sent to backend when override is configured |
| `headers` | object | Request headers (all or filtered by config) |

### Kafka Message Key

Each message uses `request_id` as the Kafka message key, ensuring all events for the same request land on the same partition.

## Disabling

To disable event publishing, simply don't set `KAFKA_BROKERS`. When no broker is configured, a no-op publisher is used with zero overhead.
