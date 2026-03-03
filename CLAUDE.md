# CLAUDE.md - Envoy AI Gateway (Fork)

## Overview

This is a fork of [Envoy AI Gateway](https://github.com/envoyproxy/ai-gateway) with custom modifications for path-based routing and multiple InferencePool support.

## Key Modifications

### Multiple InferencePool Backends with Session Affinity

Added support for multiple InferencePool backends per rule with weighted routing and session affinity. This enables:

- **Canary deployments**: Route 80% traffic to v1, 20% to v2
- **A/B testing**: Split traffic between different model versions
- **Session affinity**: Same user/session always routes to same pool (preserves KV cache locality)

**New Types Added** (`api/v1alpha1/ai_gateway_route.go`):

- `SessionAffinityConfig` - Configuration for session affinity
- `HashSource` - Where to extract hash key (Header, RequestBody, QueryParam)
- `SessionAffinityFallback` - Behavior when no hash key found (WeightedRandom, FirstBackend, RejectRequest)

**Modified Files:**

- `api/v1alpha1/ai_gateway_route.go` - Added SessionAffinity types, removed single-pool CEL validation
- `internal/extproc/session_affinity.go` - **NEW** - Consistent hashing logic
- `internal/extensionserver/weighted_inferencepool.go` - **NEW** - Weighted cluster generation
- `internal/extensionserver/post_cluster_modify.go` - Skip multiple pools (handled in PostTranslateModify)
- `internal/extensionserver/post_route_modify.go` - Handle multiple pools
- `internal/extensionserver/post_translate_modify.go` - Extract `addUpstreamExtProcFilter()`, integrate weighted clusters
- `internal/extensionserver/inferencepool.go` - Fix metadata initialization bug

**Implementation Details:**

- Weighted clusters are created in `PostTranslateModify` (not `PostClusterModify`) because Envoy Gateway generates 1 cluster per rule, not per backend
- Each weighted cluster uses `ORIGINAL_DST` type with header-based load balancing
- Upstream ext_proc filter is added to each weighted cluster for AI Gateway processing
- Route is modified to use `weighted_clusters` with proper weights
- Backend name metadata must be set on clusters for ext_proc to identify backends
- `WeightedPool` struct tracks `BackendRefIndex` to ensure metadata names match controller-generated names

### ModelNameOverride for InferencePool

`modelNameOverride` is now supported for InferencePool backends. This allows abstract model names for users while backends receive actual model names:

```yaml
backendRefs:
  - name: vllm-pool
    group: inference.networking.k8s.io
    kind: InferencePool
    modelNameOverride: "Qwen/Qwen3-0.6B"  # Backend receives this
```

**How it works with x-ai-eg-model header:**
1. User sends request with `model: "qwen3-0.6b"` in body
2. AI Gateway extracts model name → sets `x-ai-eg-model: qwen3-0.6b` header
3. Rule matches on `x-ai-eg-model` header (effectively body matching)
4. `modelNameOverride` transforms model name before sending to backend

**Example - Multiple models on same endpoint:**
```yaml
rules:
- matches:
  - headers:
    - name: x-ai-eg-model
      value: qwen3-0.6b
  backendRefs:
  - name: pool-qwen
    modelNameOverride: Qwen/Qwen3-0.6B
- matches:
  - headers:
    - name: x-ai-eg-model
      value: llama3-8b
  backendRefs:
  - name: pool-llama
    modelNameOverride: meta-llama/Meta-Llama-3-8B-Instruct
```

### Error Categorization Metrics

Added error type categorization to ExtProc metrics for better observability and alerting.

**New Metric:**
- `gen_ai.server.request.errors` - Counter of failed requests by error type

**Error Types** (`internal/metrics/genai.go`):

| Type | Description |
|------|-------------|
| `invalid_request` | Malformed/invalid request body (422 errors) |
| `backend_error` | Non-2xx response from upstream backend |
| `transform_error` | Request/response transformation failures |
| `auth_error` | Authentication/authorization failures |
| `config_error` | Backend setup/configuration errors |
| `internal_error` | CEL evaluation, metadata building errors |

**Modified Files:**
- `internal/metrics/genai.go` - Added `GenAIErrorType` enum and error counter
- `internal/metrics/metrics.go` - Updated `RecordRequestCompletion` interface
- `internal/metrics/metrics_impl.go` - Implemented error categorization
- `internal/extproc/processor_impl.go` - Added error types to all failure paths

**PromQL Examples:**
```promql
# Error rate by type
rate(gen_ai_server_request_errors_total{error_type="backend_error"}[5m])

# Total error ratio
sum(gen_ai_server_request_errors_total) / sum(gen_ai_server_request_duration_count)
```

### Path-Based Routing Support

Added `Path` field to `AIGatewayRouteRuleMatch` for Vertex AI-style URL patterns:

```
/v1/projects/{project}/locations/{region}/endpoints/{id}/completions
```

**Modified Files:**

- `api/v1alpha1/ai_gateway_route.go` - Added `Path *gwapiv1.HTTPPathMatch` field
- `internal/controller/ai_gateway_route.go` - Updated `newHTTPRoute()` to use Path from spec

## Development

### Prerequisites

- Go 1.25+
- Docker with buildx
- kubectl and kind (for e2e tests)

### Build Commands

```bash
# Generate CRDs from API types
make apigen

# Build controller binary
make build.controller

# Build external processor binary
make build.extproc

# Build aigw CLI
make build.aigw

# Run unit tests
make test

# Run all precommit checks
make precommit
```

### Docker Build (AMD64)

```bash
# Build controller for linux/amd64
make build.controller GOOS_LIST="linux" GOARCH_LIST="amd64"

# Build Docker image
docker buildx build --platform linux/amd64 \
  --build-arg COMMAND_NAME=controller \
  -t ghcr.io/thuanpham582002/ai-gateway-controller:latest \
  --push .
```

### CRD Installation

```bash
# Install AI Gateway CRDs
helm upgrade -i aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm \
  --version v0.0.0-latest \
  --namespace envoy-ai-gateway-system \
  --create-namespace

# Or from local manifests
kubectl apply -f manifests/charts/ai-gateway-crds-helm/templates/
```

## Project Structure

```
ai-gateway/
├── api/v1alpha1/           # CRD API definitions
├── cmd/
│   ├── aigw/               # CLI tool
│   ├── controller/         # Kubernetes controller
│   └── extproc/            # External processor
├── internal/
│   ├── controller/         # Controller implementation
│   └── extproc/            # ExtProc implementation
├── manifests/
│   └── charts/             # Helm charts
└── tests/                  # Test suites
```

## AIGatewayRoute Examples

### Path-Based Routing

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayRoute
metadata:
  name: route-ep-abc12345
  namespace: myproject
spec:
  parentRefs:
    - name: ai-gateway
      namespace: envoy-ai-gateway-system
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /v1/projects/myproject/locations/default/endpoints/abc12345
      backendRefs:
        - name: ep-abc12345-gpt2-inference-pool
          namespace: myproject
          group: inference.networking.k8s.io
          kind: InferencePool
          weight: 100
  llmRequestCosts:
    - metadataKey: input_tokens
      type: InputToken
    - metadataKey: output_tokens
      type: OutputToken
```

### Weighted InferencePool with Session Affinity (Canary)

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayRoute
metadata:
  name: llama3-canary
  namespace: default
spec:
  parentRefs:
    - name: ai-gateway
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: llama3-8b
      backendRefs:
        - name: vllm-llama3-8b-v1
          group: inference.networking.k8s.io
          kind: InferencePool
          weight: 80 # 80% traffic to stable version
        - name: vllm-llama3-8b-v2
          group: inference.networking.k8s.io
          kind: InferencePool
          weight: 20 # 20% traffic to canary version
      sessionAffinity:
        hashOn:
          - type: Header
            name: X-Session-ID
          - type: QueryParam
            name: user_id
```

**Session Affinity Behavior:**

- Same session ID → always same pool (preserves KV cache locality)
- Distribution across all sessions ≈ 80/20 (statistical)
- No storage needed - uses native Envoy consistent hashing

**Implementation Details:**

- Uses Envoy's native `hash_policy` + `use_hash_policy` on `weighted_clusters`
- `hash_policy` generates hash from request (header/query param)
- `use_hash_policy: true` uses that hash for cluster selection (not random)

**Supported HashOn Types:**

- `Header` - Converted to Envoy `hash_policy.header`
- `QueryParam` - Converted to Envoy `hash_policy.query_parameter`

**Fallback Behavior:**

- CRD `fallback` field is ignored
- When no hash key found, Envoy automatically uses random weighted selection

## Known Issues

### CEL Validation Budget Exceeded (Path field)

**Issue:** Adding `Path *gwapiv1.HTTPPathMatch` to `AIGatewayRouteRuleMatch` imports 11 CEL validation rules from Gateway API. Combined with existing AIGatewayRoute validations, total cost exceeds Kubernetes CRD budget (~138% of limit).

**Current Workaround:** Removed CEL validations from Path field in CRD manifest (`manifests/charts/ai-gateway-crds-helm/templates/aigateway.envoyproxy.io_aigatewayroutes.yaml`). Envoy still validates paths at runtime.

**Proper Fix (TODO):**

1. Define custom `SimplifiedPathMatch` type with minimal/no CEL validations
2. Or refactor existing AIGatewayRoute validations to reduce total cost
3. Add back essential validation: `self.value.startsWith('/')`

**Risk:** Low - invalid paths return 404 at runtime, no security issue.

### HTTPRoute Filter Ordering Fix (PathRewrite + ExtProc)

**Issue:** When using `pathRewrite` in AIGatewayRoute, ExtProc was seeing the original path instead of the rewritten path. This caused ExtProc to reject requests with custom path prefixes like `/v1/projects/{project}/locations/{region}/endpoints/{id}/completions`.

**Root Cause:** HTTPRoute filters were ordered as:

1. ExtensionRef (triggers ExtProc)
2. URLRewrite (path rewrite)

ExtProc ran BEFORE URLRewrite, so it saw the original path and rejected it as "unsupported path".

**Fix:** Reordered filters in `internal/controller/ai_gateway_route.go`:

1. URLRewrite (path rewrite) - runs first
2. ExtensionRef (triggers ExtProc) - runs after, sees rewritten path

Now ExtProc correctly sees `/v1/completions` instead of the custom-prefixed path.

### ExtProc Suffix Matching (Path Processor Lookup)

**Issue:** ExtProc used exact path matching to find processors, which failed for custom path prefixes like `/v1/projects/{project}/.../completions`.

**Root Cause:** `processorForPath()` in `internal/extproc/server.go` only did exact map lookup:

```go
newProcessor, ok := s.processorFactories[path] // exact match only
```

**Fix:** Added suffix matching fallback in `processorForPath()`:

1. First try exact match (backward compatibility)
2. If no exact match, try suffix matching against registered paths

Now `/v1/projects/myproject/locations/default/endpoints/abc123/completions` matches the processor registered for `/v1/completions`.

---

## Upstream Sync

```bash
# Add upstream remote
git remote add upstream https://github.com/envoyproxy/ai-gateway.git

# Fetch upstream changes
git fetch upstream

# Merge upstream main (resolve conflicts as needed)
git merge upstream/main
```
