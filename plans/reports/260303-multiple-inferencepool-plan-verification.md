# Implementation Plan Verification: Multiple InferencePool Backends

**Date:** 2026-03-03
**Status:** PLAN REQUIRES SIGNIFICANT REVISION
**Complexity:** HIGH (more complex than initially assumed)

---

## Executive Summary

The proposed plan to enable multiple InferencePool backends per rule is **incomplete**. The key assumption that "Envoy Gateway generates separate clusters for each backend with weights" is **INCORRECT** for InferencePool backends. The current architecture uses ORIGINAL_DST clusters which fundamentally do not support weighted load balancing in the traditional sense.

---

## Current Architecture Analysis

### How InferencePool Works Today

1. **Single Cluster Per Rule**: Envoy Gateway generates ONE cluster per HTTPRoute rule
   - Cluster naming: `httproute/<namespace>/<name>/rule/<index>`
   - NOT `httproute/<namespace>/<name>/rule/<index>/backend/<index>`

2. **ORIGINAL_DST Cluster Type**: InferencePool clusters use `ORIGINAL_DST` type
   ```go
   // post_cluster_modify.go:70
   cluster.ClusterDiscoveryType = &clusterv3.Cluster_Type{Type: clusterv3.Cluster_ORIGINAL_DST}
   cluster.LbPolicy = clusterv3.Cluster_CLUSTER_PROVIDED
   ```

3. **Header-Based Routing**: Endpoint selection via EPP (Endpoint Picker Protocol)
   - EPP sets `x-gateway-destination-endpoint` header
   - Envoy routes to destination specified in header
   - NO weighted cluster selection at Envoy level

### Why Weights Don't Work with ORIGINAL_DST

With ORIGINAL_DST clusters:
- There's no cluster-level weighted routing
- The EPP service determines which endpoint receives traffic
- Weights would need to be handled BY the EPP, not Envoy

---

## Detailed Plan Analysis

### Phase 1: API Layer (CORRECT but INSUFFICIENT)

**Proposed:**
- Remove CEL validation at line 203: `size(self.backendRefs) == 1`
- Update comments at lines 216-217

**Assessment:** CORRECT
This allows multiple InferencePools in API spec. However, simply removing validation won't make weighted routing work.

**Code Location:**
```go
// api/v1alpha1/ai_gateway_route.go:202-203
// +kubebuilder:validation:XValidation:rule="...size(self.backendRefs) == 1",
//   message="only one InferencePool backend is allowed per rule"
```

### Phase 2: PostClusterModify (PROBLEMATIC)

**Proposed:**
- Remove `len(inferencePools) != 1` check at lines 49-51

**Current Code:**
```go
// post_cluster_modify.go:48-52
if inferencePools := s.constructInferencePoolsFrom(...); inferencePools != nil {
    if len(inferencePools) != 1 {
        return nil, fmt.Errorf("BUG: at most one inferencepool can be referenced per route rule")
    }
    s.handleInferencePoolCluster(req.Cluster, inferencePools[0])
}
```

**Critical Issue:**
- Envoy Gateway sends ONE cluster in `PostClusterModify` request
- All backends in a rule map to THE SAME cluster
- There's no mechanism to create separate clusters per InferencePool backend here

**Required Changes:**
1. Cannot simply iterate over multiple pools for ONE cluster
2. Need architectural decision: How to handle multiple EPPs for weighted traffic?

### Phase 3: maybeModifyCluster (PROBLEMATIC)

**Proposed:**
- Fix hard-coded `BackendRefs[0]` at lines 257-261

**Current Code:**
```go
// post_translate_modify.go:257-261
} else {
    // we can only specify one backend in a rule for InferencePool.
    backendRef := httpRouteRule.BackendRefs[0]
    setClusterMetadataBackendName(cluster, aigwRoute.Namespace, backendRef.Name, ...)
}
```

**Critical Issue:**
- This code runs AFTER PostClusterModify
- Still dealing with ONE cluster
- Setting metadata for multiple pools on same cluster is semantically unclear

### Phase 4: PostRouteModify (NOT IN PLAN - MISSING)

**Current Code:**
```go
// post_route_modify.go:34-48
if inferencePools != nil {
    if len(inferencePools) != 1 {
        return nil, fmt.Errorf("BUG: at most one inferencepool can be referenced per route rule but found %d", len(inferencePools))
    }
    // ... sets metadata for inferencePools[0]
    buildEPPMetadataForRoute(req.Route, inferencePools[0])
}
```

**Missing from plan:** This file also enforces single InferencePool and must be updated.

---

## Fundamental Architecture Problem

### The Weighted Traffic Question

With multiple InferencePools, what does "weighted traffic" mean?

**Scenario A: Different EPP Services**
- Pool-v1: EPP service-A picks endpoint
- Pool-v2: EPP service-B picks endpoint
- Question: Who decides which EPP to consult?

**Scenario B: Same Model, Different Versions**
- Pool-v1: Model version 1.0
- Pool-v2: Model version 2.0
- Expected: 80% → v1, 20% → v2

### Current ORIGINAL_DST Doesn't Support This

```
Request → Envoy → [Single ORIGINAL_DST Cluster] → EPP → Endpoint

With weights desired:
Request → Envoy → [80% to EPP-A, 20% to EPP-B] → EPP → Endpoint
```

This requires **EDS-based weighted cluster selection BEFORE hitting ORIGINAL_DST**.

---

## Possible Solutions

### Option 1: Route-Level Weighted Splitting (RECOMMENDED)

Create multiple HTTPRoute rules instead of multiple backends per rule:

```yaml
rules:
  - matches: [{headers: [{name: x-ai-eg-model, value: llama}]}]
    backendRefs:
      - name: pool-v1
        group: inference.networking.k8s.io
        kind: InferencePool
        weight: 80
  - matches: [{headers: [{name: x-ai-eg-model, value: llama}]}]
    backendRefs:
      - name: pool-v2
        group: inference.networking.k8s.io
        kind: InferencePool
        weight: 20
```

**Implementation:**
- Controller duplicates rules when multiple InferencePools detected
- Each rule gets one InferencePool backend with appropriate weight
- Envoy Gateway handles route-level weighted routing natively

**Pros:**
- Works with existing cluster architecture
- Envoy Gateway route-level weight splitting works natively
- Minimal changes to extension server

**Cons:**
- Requires controller-level transformation
- Rule explosion (N InferencePools = N rules)

### Option 2: Composite EPP Cluster (COMPLEX)

Create a weighted cluster config that routes to different EPP services:

**Implementation:**
- Build aggregate cluster with multiple EPP endpoints
- Configure endpoint-level weights
- Requires significant xDS cluster manipulation

**Pros:**
- Single rule semantics preserved

**Cons:**
- Complex xDS manipulation
- May conflict with ORIGINAL_DST semantics
- Need to verify Envoy supports weighted ORIGINAL_DST

### Option 3: Single EPP with Pool Selection (EXTERNAL)

Delegate pool selection to EPP service:

**Implementation:**
- Pass pool weights as metadata to EPP
- EPP implements weighted selection logic

**Pros:**
- No Envoy config changes
- EPP can implement sophisticated selection

**Cons:**
- Requires EPP service changes
- Outside AI Gateway scope
- Breaks compatibility with standard EPP implementations

---

## Revised Implementation Plan

### Phase 1: API Layer Changes

**File:** `api/v1alpha1/ai_gateway_route.go`

1. **KEEP** the `size(self.backendRefs) == 1` CEL validation (for now)
2. **ADD** new validation message explaining the limitation
3. Document that weighted InferencePool routing requires separate rules

### Phase 2: Documentation Update

Document the pattern for weighted InferencePool routing:

```yaml
# Weighted InferencePool routing - use separate rules
spec:
  rules:
    # 80% traffic to pool-v1
    - matches:
        - headers: [{name: x-ai-eg-model, value: llama}]
      backendRefs:
        - name: pool-v1
          kind: InferencePool
          group: inference.networking.k8s.io
          weight: 80
    # 20% traffic to pool-v2 (same match criteria)
    - matches:
        - headers: [{name: x-ai-eg-model, value: llama}]
      backendRefs:
        - name: pool-v2
          kind: InferencePool
          group: inference.networking.k8s.io
          weight: 20
```

### Phase 3 (FUTURE): Controller-Level Rule Splitting

If single-rule-multiple-InferencePool UX is required:

**File:** `internal/controller/ai_gateway_route.go`

Add transformation in `newHTTPRoute()`:
```go
// When rule has multiple InferencePool backends, split into multiple rules
for i, rule := range aiGatewayRoute.Spec.Rules {
    if hasMultipleInferencePools(rule.BackendRefs) {
        splitRules := splitInferencePoolRule(rule)
        rules = append(rules, splitRules...)
    } else {
        rules = append(rules, convertRule(rule))
    }
}
```

---

## Files Requiring Changes (Current Plan)

| File | Line(s) | Change Type | Status |
|------|---------|-------------|--------|
| `api/v1alpha1/ai_gateway_route.go` | 203 | CEL Validation | DEFER |
| `api/v1alpha1/ai_gateway_route.go` | 216-217 | Comment Update | OPTIONAL |
| `internal/extensionserver/post_cluster_modify.go` | 49-51 | Remove Check | NOT SUFFICIENT |
| `internal/extensionserver/post_translate_modify.go` | 257-261 | Loop Multiple | NOT SUFFICIENT |
| `internal/extensionserver/post_route_modify.go` | 35-36 | Remove Check | NOT ADDRESSED |
| `tests/crdcel/testdata/aigatewayroutes/inference_pool_multiple.yaml` | - | Update Test | DEFER |
| `tests/crdcel/main_test.go` | 45-47 | Expect Success | DEFER |

---

## Recommendations

### Immediate Actions

1. **REJECT** current plan as-is - fundamentally incomplete
2. **DOCUMENT** current limitation clearly
3. **PROVIDE** workaround documentation (separate rules pattern)

### Short-term (If Feature Required)

1. Implement Option 1 (Route-Level Rule Splitting) in controller
2. Update validation to accept multiple InferencePools
3. Add e2e tests for weighted routing

### Testing Requirements (If Implemented)

1. Unit tests for rule splitting logic
2. Integration tests verifying xDS config
3. e2e tests with actual weighted traffic verification

---

## Unresolved Questions

1. **UX Priority**: Is single-rule-multiple-backend UX essential, or is separate-rules acceptable?
2. **EPP Compatibility**: Do standard EPP implementations handle multiple pool metadata?
3. **Weight Semantics**: Are weights expected to work per-request or per-connection?

---

## Conclusion

The original plan is **architecturally unsound** due to fundamental misunderstanding of how ORIGINAL_DST clusters work with InferencePool/EPP pattern. A proper solution requires either:

1. **Accept limitation** and document workaround (separate rules)
2. **Implement rule splitting** in controller layer

Both options are viable. Option 1 is immediate (documentation only). Option 2 requires ~2-3 days of development and testing.

---

*Generated by Claude Code Planning Agent*
