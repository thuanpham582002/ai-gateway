# AI Gateway OTEL Metrics & Tracing Setup

## Overview

AI Gateway uses OpenTelemetry for LLM metrics and tracing. Token counts, model info, and request/response data are exported via OTEL spans.

**Metrics captured:**
- `llm.token_count.prompt` - Input tokens
- `llm.token_count.completion` - Output tokens
- `llm.token_count.total` - Total tokens
- `llm.model_name` - Model used
- `llm.system` - LLM provider (openai, anthropic, etc.)
- Request/response content, latency, trace IDs

## Configuration

### Step 1: Create GatewayConfig

```yaml
# gatewayconfig.yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: GatewayConfig
metadata:
  name: ai-gateway-config
  namespace: model-serving  # Same namespace as Gateway
spec:
  extProc:
    kubernetes:
      env:
        # Option A: Console (debugging)
        - name: OTEL_TRACES_EXPORTER
          value: "console"
        - name: OTEL_SERVICE_NAME
          value: "ai-gateway"

        # Option B: OTLP endpoint (production) - use HTTP protocol on port 4318
        # - name: OTEL_EXPORTER_OTLP_ENDPOINT
        #   value: "http://otel-collector.observability:4318"
        # - name: OTEL_EXPORTER_OTLP_PROTOCOL
        #   value: "http/protobuf"
        # - name: OTEL_TRACES_EXPORTER
        #   value: "otlp"
        # - name: OTEL_SERVICE_NAME
        #   value: "ai-gateway"
```

```bash
kubectl apply -f gatewayconfig.yaml
```

### Step 2: Annotate Gateway

```bash
kubectl annotate gateway ai-gateway -n model-serving \
  aigateway.envoyproxy.io/gateway-config=ai-gateway-config --overwrite
```

### Step 3: Restart ExtProc Pod

```bash
# Find and delete the envoy pod to pick up new config
kubectl delete pod -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=ai-gateway
```

### Step 4: Verify Configuration

```bash
# Check env vars are applied
kubectl get pod -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=ai-gateway \
  -o jsonpath='{.items[0].spec.containers[?(@.name=="ai-gateway-extproc")].env}' | jq .

# Check logs for OTEL output (console exporter)
kubectl logs -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=ai-gateway \
  -c ai-gateway-extproc --tail=50 | grep -E "Span|token"
```

## Test Commands

### Single Request Test

```bash
# Chat completions
curl -X POST "http://<GATEWAY_IP>/projects/testproject/locations/default/endpoints/631844dd/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: test-session-123" \
  -d '{"model": "qwen3-0.6b", "messages": [{"role": "user", "content": "Hello"}]}'
```

### Load Test (100 parallel requests)

```bash
seq 1 100 | xargs -n1 -P100 -I{} curl -s -o /dev/null \
  "http://<GATEWAY_IP>/projects/testproject/locations/default/endpoints/631844dd/v1/completions" \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: test-{}" \
  -d '{"model":"qwen3-0.6b","prompt":"Hi","max_tokens":1}'
```

### Check Envoy Stats

```bash
# Port-forward to envoy admin
kubectl port-forward -n envoy-gateway-system \
  deploy/envoy-model-serving-ai-gateway-ea0020c9 19000:19000 &

# Check request counts per pool
curl -s http://localhost:19000/stats | grep "upstream_rq_total"

# Check ext_proc stats
curl -s http://localhost:19000/stats | grep "ext_proc"

# Cleanup
pkill -f "port-forward.*19000"
```

---

## Production Setup: OTEL Collector + Jaeger

### Deploy Jaeger (All-in-One)

```yaml
# jaeger.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: observability
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jaeger
  namespace: observability
spec:
  replicas: 1
  selector:
    matchLabels:
      app: jaeger
  template:
    metadata:
      labels:
        app: jaeger
    spec:
      containers:
        - name: jaeger
          image: jaegertracing/all-in-one:1.54
          ports:
            - containerPort: 16686  # UI
            - containerPort: 4317   # OTLP gRPC
            - containerPort: 4318   # OTLP HTTP
          env:
            - name: COLLECTOR_OTLP_ENABLED
              value: "true"
---
apiVersion: v1
kind: Service
metadata:
  name: jaeger
  namespace: observability
spec:
  selector:
    app: jaeger
  ports:
    - name: ui
      port: 16686
      targetPort: 16686
    - name: otlp-grpc
      port: 4317
      targetPort: 4317
    - name: otlp-http
      port: 4318
      targetPort: 4318
```

```bash
kubectl apply -f jaeger.yaml
```

### Update GatewayConfig for Jaeger

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: GatewayConfig
metadata:
  name: ai-gateway-config
  namespace: model-serving
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://jaeger.observability:4318"  # Use HTTP port
        - name: OTEL_EXPORTER_OTLP_PROTOCOL
          value: "http/protobuf"
        - name: OTEL_TRACES_EXPORTER
          value: "otlp"
        - name: OTEL_SERVICE_NAME
          value: "ai-gateway"
        # Optional: Set sampling rate
        - name: OTEL_TRACES_SAMPLER
          value: "always_on"
```

```bash
kubectl apply -f gatewayconfig.yaml
kubectl delete pod -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=ai-gateway
```

### Access Jaeger UI

```bash
kubectl port-forward -n observability svc/jaeger 16686:16686
# Open http://localhost:16686
```

---

## Production Setup: OTEL Collector + Prometheus

For metrics (not just traces), deploy OTEL Collector with Prometheus exporter.

### Deploy OTEL Collector

```yaml
# otel-collector.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: otel-collector-config
  namespace: observability
data:
  config.yaml: |
    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318

    processors:
      batch:
        timeout: 1s
        send_batch_size: 1024

      # Convert spans to metrics
      spanmetrics:
        metrics_exporter: prometheus
        dimensions:
          - name: llm.model_name
          - name: llm.system
        histogram:
          explicit:
            buckets: [100ms, 500ms, 1s, 5s, 10s, 30s, 60s]

    exporters:
      prometheus:
        endpoint: 0.0.0.0:8889
        namespace: ai_gateway

      # Also export traces to Jaeger
      otlp/jaeger:
        endpoint: jaeger.observability:4317
        tls:
          insecure: true

    service:
      pipelines:
        traces:
          receivers: [otlp]
          processors: [batch, spanmetrics]
          exporters: [otlp/jaeger]
        metrics:
          receivers: [otlp]
          processors: [batch]
          exporters: [prometheus]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: otel-collector
  namespace: observability
spec:
  replicas: 1
  selector:
    matchLabels:
      app: otel-collector
  template:
    metadata:
      labels:
        app: otel-collector
    spec:
      containers:
        - name: collector
          image: otel/opentelemetry-collector-contrib:0.96.0
          args: ["--config=/etc/otel/config.yaml"]
          ports:
            - containerPort: 4317   # OTLP gRPC
            - containerPort: 4318   # OTLP HTTP
            - containerPort: 8889   # Prometheus metrics
          volumeMounts:
            - name: config
              mountPath: /etc/otel
      volumes:
        - name: config
          configMap:
            name: otel-collector-config
---
apiVersion: v1
kind: Service
metadata:
  name: otel-collector
  namespace: observability
spec:
  selector:
    app: otel-collector
  ports:
    - name: otlp-grpc
      port: 4317
    - name: otlp-http
      port: 4318
    - name: prometheus
      port: 8889
```

```bash
kubectl apply -f otel-collector.yaml
```

### Update GatewayConfig for OTEL Collector

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: GatewayConfig
metadata:
  name: ai-gateway-config
  namespace: model-serving
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector.observability:4318"  # Use HTTP port
        - name: OTEL_EXPORTER_OTLP_PROTOCOL
          value: "http/protobuf"
        - name: OTEL_TRACES_EXPORTER
          value: "otlp"
        - name: OTEL_SERVICE_NAME
          value: "ai-gateway"
```

### Configure Prometheus to Scrape OTEL Collector

Add to Prometheus config:

```yaml
scrape_configs:
  - job_name: 'otel-collector'
    static_configs:
      - targets: ['otel-collector.observability:8889']
```

### Prometheus Metrics Available

After setup, these metrics are available:

```promql
# Request latency histogram
ai_gateway_span_duration_bucket{llm_model_name="Qwen/Qwen3-0.6B"}

# Request count by model
ai_gateway_span_count{llm_model_name="Qwen/Qwen3-0.6B"}

# Token usage (from span attributes - requires custom processing)
# Note: Token counts are in span attributes, need spanmetrics processor
```

---

---

## Billing by Session/API Key

To track token usage per session or API key for billing purposes:

### Step 1: Add Header Attribute Mapping to Controller

Patch the controller deployment to map HTTP headers to span/metric attributes:

```bash
kubectl patch deploy ai-gateway-controller -n envoy-ai-gateway-system --type='json' -p='[
  {"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--spanRequestHeaderAttributes=x-session-id:session.id,x-api-key:api.key,authorization:auth.token"},
  {"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--metricsRequestHeaderAttributes=x-session-id:session.id,x-api-key:api.key"}
]'
```

Or via Helm values:

```yaml
controller:
  spanRequestHeaderAttributes: "x-session-id:session.id,x-api-key:api.key"
  metricsRequestHeaderAttributes: "x-session-id:session.id,x-api-key:api.key"
```

**Format:** `header-name:attribute-name,header-name2:attribute-name2`

### Step 2: Update OTEL Collector Dimensions

Add the new attributes to spanmetrics connector:

```yaml
# otel-collector configmap
connectors:
  spanmetrics:
    dimensions:
      - name: llm.model_name
      - name: llm.system
      - name: session.id      # From X-Session-ID header
      - name: api.key         # From X-API-Key header
```

Full ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: otel-collector-config
  namespace: observability
data:
  config.yaml: |
    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318

    processors:
      batch:
        timeout: 1s

    connectors:
      spanmetrics:
        histogram:
          explicit:
            buckets: [100ms, 500ms, 1s, 5s, 10s, 30s, 60s]
        dimensions:
          - name: llm.model_name
          - name: llm.system
          - name: session.id
          - name: api.key

    exporters:
      prometheus:
        endpoint: 0.0.0.0:8889
        namespace: ai_gateway
      otlp/jaeger:
        endpoint: jaeger.observability:4317
        tls:
          insecure: true

    service:
      pipelines:
        traces:
          receivers: [otlp]
          processors: [batch]
          exporters: [spanmetrics, otlp/jaeger]
        metrics:
          receivers: [spanmetrics]
          exporters: [prometheus]
```

### Step 3: Restart Pods

```bash
kubectl rollout restart deploy/ai-gateway-controller -n envoy-ai-gateway-system
kubectl rollout restart deploy/otel-collector -n observability
kubectl delete pod -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=ai-gateway
```

### Step 4: Test with Headers

```bash
curl -X POST "http://<GATEWAY_IP>/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: user-123-billing" \
  -H "X-API-Key: sk-test-key-abc" \
  -d '{"model": "qwen3-0.6b", "messages": [{"role": "user", "content": "Hi"}]}'
```

### Prometheus Metrics for Billing

After configuration, these metrics are available:

```promql
# Total calls by API key
ai_gateway_calls_total{api_key="sk-test-key-abc"}

# Total calls by session
ai_gateway_calls_total{session_id="user-123-billing"}

# Latency by API key and model
ai_gateway_duration_milliseconds_sum{api_key="sk-test-key-abc", llm_model_name="Qwen/Qwen3-0.6B"}

# Calls per model per API key
sum by (api_key, llm_model_name) (ai_gateway_calls_total)
```

### Token-Based Billing (Advanced)

Token counts are in span attributes (`llm.token_count.prompt`, `llm.token_count.completion`).
To export as metrics, use OTEL Collector's `transform` processor:

```yaml
processors:
  transform:
    trace_statements:
      - context: span
        statements:
          - set(attributes["llm.tokens.total"], attributes["llm.token_count.total"])
```

Or query token data from Jaeger traces via API.

### Native Token Metrics (Best for Billing)

AI Gateway exports native Prometheus metrics on ExtProc admin port (1064):

**Metrics:**
- `gen_ai_client_token_usage_sum` - Total tokens (histogram sum)
- `gen_ai_server_request_duration_seconds` - Request latency

**Labels:**
- `session_id`, `api_key` - From header mapping
- `gen_ai_token_type` - `input` or `output`
- `gen_ai_request_model` - Model name
- `gen_ai_provider_name` - Provider (openai, anthropic, etc.)

**PromQL for Billing:**

```promql
# Total input tokens per user per model
sum by (gen_ai_request_model) (
  gen_ai_client_token_usage_sum{
    session_id="user-123",
    gen_ai_token_type="input"
  }
)

# Total output tokens per API key
sum(gen_ai_client_token_usage_sum{
  api_key="sk-xxx",
  gen_ai_token_type="output"
})

# Top 10 users by total tokens
topk(10, sum by (session_id) (gen_ai_client_token_usage_sum))

# Token usage breakdown per user
sum by (session_id, gen_ai_request_model, gen_ai_token_type) (
  gen_ai_client_token_usage_sum
)
```

**Prometheus Scrape Config:**

```yaml
scrape_configs:
  - job_name: 'ai-gateway-extproc'
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: ['envoy-gateway-system']
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_container_name]
        regex: ai-gateway-extproc
        action: keep
      - source_labels: [__meta_kubernetes_pod_container_port_name]
        regex: aigw-admin
        action: keep
```

---

## EPP Metrics (InferencePool Endpoint Picker)

EPP exposes metrics on port 9090 but requires **RBAC authentication**.

### Problem

```bash
curl http://epp-service:9090/metrics
# Returns: "Unauthorized"
```

EPP uses Kubernetes TokenReview for authentication. Without proper RBAC, metrics access is denied.

### Solution: Create RBAC for Prometheus

```yaml
# epp-metrics-rbac.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: epp-metrics-reader
rules:
- nonResourceURLs:
  - /metrics
  verbs:
  - get
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: prometheus-epp-metrics
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: epp-metrics-reader
subjects:
- kind: ServiceAccount
  name: prometheus-server  # Your Prometheus SA
  namespace: monitoring    # Your Prometheus namespace
```

```bash
kubectl apply -f epp-metrics-rbac.yaml
```

### Prometheus Scrape Config with Bearer Token

```yaml
scrape_configs:
  - job_name: 'epp'
    kubernetes_sd_configs:
      - role: endpoints
        namespaces:
          names: ['testproject']  # Your namespace
    relabel_configs:
      - source_labels: [__meta_kubernetes_service_name]
        regex: '.*epp-service'
        action: keep
      - source_labels: [__meta_kubernetes_endpoint_port_name]
        regex: metrics
        action: keep
    bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
```

### Key EPP Metrics

| Metric | Description |
|--------|-------------|
| `inference_pool_ready_pods` | Ready pods in pool |
| `inference_pool_average_kv_cache_utilization` | Avg KV cache % |
| `inference_pool_average_queue_size` | Avg queue depth |
| `inference_pool_per_pod_queue_size` | Per-pod queue |
| `inference_objective_request_total` | Total requests |
| `inference_objective_running_requests` | In-flight requests |
| `inference_objective_input_tokens_sum` | Total input tokens |
| `inference_objective_output_tokens_sum` | Total output tokens |
| `inference_extension_scheduler_attempts_total` | Scheduling attempts |

### Verify Access

```bash
# Test with ServiceAccount token
kubectl run curl-epp --rm -i --restart=Never \
  --image=curlimages/curl -n <namespace> -- sh -c '
TOKEN=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
curl -s -H "Authorization: Bearer $TOKEN" http://<epp-service>:9090/metrics | head -50
'
```

---

## Environment Variables Reference

| Variable | Description | Example |
|----------|-------------|---------|
| `OTEL_TRACES_EXPORTER` | Exporter type | `console`, `otlp`, `none` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint | `http://collector:4317` |
| `OTEL_SERVICE_NAME` | Service name in traces | `ai-gateway` |
| `OTEL_TRACES_SAMPLER` | Sampling strategy | `always_on`, `parentbased_traceidratio` |
| `OTEL_TRACES_SAMPLER_ARG` | Sampler argument | `0.1` (10% sampling) |
| `OTEL_SDK_DISABLED` | Disable OTEL entirely | `true` |

---

## Troubleshooting

### No traces appearing

```bash
# Check env vars are set
kubectl exec -n envoy-gateway-system <pod> -c ai-gateway-extproc -- env | grep OTEL

# Check extproc logs for errors
kubectl logs -n envoy-gateway-system <pod> -c ai-gateway-extproc | grep -i error
```

### Connection refused to collector

```bash
# Test connectivity from extproc pod
kubectl exec -n envoy-gateway-system <pod> -c ai-gateway-extproc -- \
  wget -qO- http://otel-collector.observability:4317 || echo "Connection failed"
```

### High memory usage

Reduce batch size or enable sampling:

```yaml
env:
  - name: OTEL_TRACES_SAMPLER
    value: "parentbased_traceidratio"
  - name: OTEL_TRACES_SAMPLER_ARG
    value: "0.1"  # Sample 10% of traces
```
