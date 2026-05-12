# External Authentication Setup

This guide covers setting up external authentication for AI Gateway using Envoy's ext_authz filter with CMP Backend.

## Overview

AI Gateway delegates authentication to CMP Backend via Envoy's **ext_authz** filter. The auth service validates API keys (OpenAI-compatible format) and returns allow/deny decisions.

**CMP Backend Endpoint:** `POST /v2/ext-auth/`

## Auth Service API Contract

### Protocol Options

| Protocol | Endpoint | Use Case |
|----------|----------|----------|
| HTTP | `POST /auth` | Simple, easy to implement |
| gRPC | `Check()` | Lower latency, streaming |

### HTTP Auth Service

**Request (from Envoy):**
```http
POST /auth HTTP/1.1
Host: auth-service:8080
X-Original-URI: /v1/chat/completions
X-Original-Method: POST
Authorization: Bearer <api-key>
X-API-Key: <api-key>
X-Forwarded-For: 10.0.0.1
```

**Response - Allow:**
```http
HTTP/1.1 200 OK
X-API-Key-ID: key_abc123
X-Org-ID: org_xyz789
```

**Response - Deny:**
```http
HTTP/1.1 401 Unauthorized
Content-Type: application/json

{"error": "invalid_api_key", "message": "API key not found or expired"}
```

## Implementation

### Go Auth Service

```go
package main

import (
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "net/http"
    "strings"
)

type APIKeyInfo struct {
    ID       string
    OrgID    string
    Scopes   []string
    RateLimit int
    Expired  bool
}

func authHandler(w http.ResponseWriter, r *http.Request) {
    // Extract API key from headers
    apiKey := r.Header.Get("X-API-Key")
    if apiKey == "" {
        apiKey = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
    }

    if apiKey == "" {
        w.WriteHeader(401)
        json.NewEncoder(w).Encode(map[string]string{
            "error": "missing_api_key",
        })
        return
    }

    // Hash key for DB lookup (never store plain keys)
    keyHash := hashKey(apiKey)

    // Lookup in DB/cache
    keyInfo, err := lookupAPIKey(keyHash)
    if err != nil || keyInfo == nil {
        w.WriteHeader(401)
        json.NewEncoder(w).Encode(map[string]string{
            "error": "invalid_api_key",
        })
        return
    }

    // Check expiration
    if keyInfo.Expired {
        w.WriteHeader(403)
        json.NewEncoder(w).Encode(map[string]string{
            "error": "api_key_expired",
        })
        return
    }

    // Allow - inject metadata for downstream
    w.Header().Set("X-API-Key-ID", keyInfo.ID)
    w.Header().Set("X-Org-ID", keyInfo.OrgID)
    w.Header().Set("X-Scopes", strings.Join(keyInfo.Scopes, ","))
    w.WriteHeader(200)
}

func hashKey(key string) string {
    h := sha256.Sum256([]byte(key))
    return hex.EncodeToString(h[:])
}

func main() {
    http.HandleFunc("/auth", authHandler)
    http.ListenAndServe(":8080", nil)
}
```

### Database Schema

```sql
CREATE TABLE api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash VARCHAR(64) NOT NULL UNIQUE,  -- SHA256 of plain key
    org_id UUID NOT NULL,
    name VARCHAR(255),
    scopes TEXT[] DEFAULT '{}',
    rate_limit INT DEFAULT 1000,           -- requests per minute
    expires_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    last_used_at TIMESTAMP
);

CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX idx_api_keys_org ON api_keys(org_id);
```

## Envoy Gateway Configuration

### SecurityPolicy

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: SecurityPolicy
metadata:
  name: api-key-auth
  namespace: envoy-ai-gateway-system
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: ai-gateway
  extAuth:
    http:
      backendRef:
        name: cmp-backend-ai-platform
        namespace: ai-platform
        port: 8000
      headersToBackend:
        - Authorization  # Bearer sk-xxx (OpenAI format)
      path: /v2/ext-auth/
```

### Auth Service Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: auth-service
  namespace: auth
spec:
  replicas: 2
  selector:
    matchLabels:
      app: auth-service
  template:
    metadata:
      labels:
        app: auth-service
    spec:
      containers:
        - name: auth
          image: your-registry/auth-service:latest
          ports:
            - containerPort: 8080
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: auth-db
                  key: url
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 256Mi
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 5
          readinessProbe:
            httpGet:
              path: /ready
              port: 8080
            initialDelaySeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: auth-service
  namespace: auth
spec:
  selector:
    app: auth-service
  ports:
    - port: 8080
      targetPort: 8080
```

## Best Practices

### Performance

| Requirement | Recommendation |
|-------------|----------------|
| Latency | < 50ms p99 (blocks every request) |
| Caching | Cache valid keys in Redis (TTL 5-15min) |
| Connection pool | Reuse DB connections |
| Replicas | Min 2 for HA |

### Security

- **Never store plain API keys** - store SHA256 hash only
- **Generate secure keys** - use `crypto/rand`, min 32 bytes
- **Rotate keys** - support multiple active keys per org
- **Audit logging** - log key usage for compliance

### Key Generation Example

```go
import (
    "crypto/rand"
    "encoding/base64"
)

func generateAPIKey() (string, error) {
    bytes := make([]byte, 32)
    if _, err := rand.Read(bytes); err != nil {
        return "", err
    }
    return "sk_" + base64.RawURLEncoding.EncodeToString(bytes), nil
}
// Output: sk_7Kx9mN2pQ4rT6vW8yA0cE3fG5hI1jL4n
```

## Response Headers

Headers injected by auth service are available to:
- AI Gateway ExtProc (for routing decisions)
- Upstream backends
- Metrics/logging

| Header | Description |
|--------|-------------|
| `X-API-Key-ID` | Unique key identifier |
| `X-Org-ID` | Organization/tenant ID |
| `X-Scopes` | Comma-separated permissions |
| `X-Rate-Limit` | Rate limit for this key |

## Error Responses

| Status | Error Code | Description |
|--------|------------|-------------|
| 401 | `missing_api_key` | No API key provided |
| 401 | `invalid_api_key` | Key not found in DB |
| 403 | `api_key_expired` | Key past expiration date |
| 403 | `quota_exceeded` | Rate limit reached |
| 403 | `insufficient_scope` | Key lacks required scope |

## Testing

```bash
# Test with valid key
curl -H "X-API-Key: sk_test_abc123" http://localhost:8080/v1/chat/completions

# Test auth service directly
curl -X POST http://auth-service:8080/auth \
  -H "X-API-Key: sk_test_abc123" \
  -H "X-Original-URI: /v1/chat/completions"
```
