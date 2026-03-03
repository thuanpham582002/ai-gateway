# CLAUDE.md - Envoy AI Gateway (Fork)

## Overview

This is a fork of [Envoy AI Gateway](https://github.com/envoyproxy/ai-gateway) with custom modifications for path-based routing support.

## Key Modifications

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

## AIGatewayRoute Example (Path-Based)

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

## Known Issues

### CEL Validation Budget Exceeded (Path field)

**Issue:** Adding `Path *gwapiv1.HTTPPathMatch` to `AIGatewayRouteRuleMatch` imports 11 CEL validation rules from Gateway API. Combined with existing AIGatewayRoute validations, total cost exceeds Kubernetes CRD budget (~138% of limit).

**Current Workaround:** Removed CEL validations from Path field in CRD manifest (`manifests/charts/ai-gateway-crds-helm/templates/aigateway.envoyproxy.io_aigatewayroutes.yaml`). Envoy still validates paths at runtime.

**Proper Fix (TODO):**
1. Define custom `SimplifiedPathMatch` type with minimal/no CEL validations
2. Or refactor existing AIGatewayRoute validations to reduce total cost
3. Add back essential validation: `self.value.startsWith('/')`

**Risk:** Low - invalid paths return 404 at runtime, no security issue.

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
