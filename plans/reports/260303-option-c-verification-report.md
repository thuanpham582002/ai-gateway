# Option C Verification Report: Multiple InferencePool Backends

**Date:** 2026-03-03
**Status:** ARCHITECTURALLY FEASIBLE WITH MODIFICATIONS
**Estimated Effort:** 8-12 hours (more conservative than original estimate)

---

## Executive Summary

Option C (weighted clusters at xDS level in PostTranslateModify) is **technically feasible** but requires **significant modifications** to the original plan. Key findings:

1. **YES** - PostTranslateModify CAN create new clusters and append to response
2. **YES** - Routes CAN be modified to use `weighted_clusters` ClusterSpecifier
3. **PARTIAL** - EPP filter handling needs more sophisticated per-route config
4. **MISSING** - Several files with `len != 1` checks not addressed in plan

---

## Verification Results

### Q1: Can PostTranslateModify Create NEW Clusters?

**Answer: YES**

**Evidence from `post_translate_modify.go:66-72`:**
```go
// Add external processor clusters for InferencePool backends.
cs, err := buildClustersForInferencePoolEndpointPickers(req.Clusters)
if err != nil {
    return nil, fmt.Errorf("failed to build clusters for InferencePool endpoint pickers: %w", err)
}
req.Clusters = append(req.Clusters, cs...)
```

**Evidence from `post_translate_modify.go:97-133`:**
```go
req.Clusters = append(req.Clusters, &clusterv3.Cluster{
    Name:                 extProcUDSClusterName,
    ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STATIC},
    // ... full cluster definition
})
```

**Evidence from response construction `post_translate_modify.go:144`:**
```go
response := &egextension.PostTranslateModifyResponse{
    Clusters: req.Clusters,  // Modified clusters returned
    Secrets: req.Secrets,
    Listeners: req.Listeners,
    Routes: req.Routes
}
```

**Conclusion:** PostTranslateModify already creates and appends new clusters. This pattern can be extended for weighted InferencePool clusters.

---

### Q2: Can Routes Be Modified to Use weighted_clusters?

**Answer: YES**

**Go Control Plane API supports:**
```go
// From go doc output
type RouteAction struct {
    // ClusterSpecifier can be:
    //   *RouteAction_Cluster              (single cluster)
    //   *RouteAction_WeightedClusters     (weighted clusters)
    //   *RouteAction_ClusterHeader
    //   ...
}

type WeightedCluster struct {
    Clusters []*WeightedCluster_ClusterWeight
    TotalWeight *wrapperspb.UInt32Value // deprecated
}
```

**Implementation approach:**
```go
// Modify route's ClusterSpecifier from single to weighted
routeAction := route.GetRoute()
if routeAction != nil {
    // Current: routeAction.ClusterSpecifier = &routev3.RouteAction_Cluster{...}
    // Change to:
    routeAction.ClusterSpecifier = &routev3.RouteAction_WeightedClusters{
        WeightedClusters: &routev3.WeightedCluster{
            Clusters: []*routev3.WeightedCluster_ClusterWeight{
                {Name: "pool-v1-cluster", Weight: wrapperspb.UInt32(80)},
                {Name: "pool-v2-cluster", Weight: wrapperspb.UInt32(20)},
            },
        },
    }
}
```

**Key insight:** The route modification happens in `maybeModifyListenerAndRoutes()` which already iterates over routes. The existing pattern can be extended.

---

### Q3: How Does EPP Filter Per-Route Enabling Work?

**Current Implementation in `patchVirtualHostWithInferencePool()` (lines 538-575):**

```go
// For routes WITHOUT matching InferencePool, DISABLE the EPP filter
override := &extprocv3.ExtProcPerRoute{
    Override: &extprocv3.ExtProcPerRoute_Disabled{Disabled: true},
}
overrideAny, _ := toAny(override)

// For each route:
if inferencePool == nil {
    // Disable ALL EPP filters for non-InferencePool routes
    for key, pool := range inferenceMatrix {
        route.TypedPerFilterConfig[key] = overrideAny
    }
} else {
    // Disable EPP filters for OTHER pools (not this route's pool)
    for key, pool := range inferenceMatrix {
        if key != httpFilterNameForInferencePool(inferencePool) {
            route.TypedPerFilterConfig[key] = overrideAny
        }
    }
}
```

**Challenge for Multiple Pools:**

When a route targets MULTIPLE InferencePools with weights, the current logic assumes ONE pool per route. Need modification:

```go
// New logic for weighted multiple pools
routePools := getInferencePoolsByMetadata(route.Metadata) // Returns []*Pool
enabledFilters := make(map[string]bool)
for _, pool := range routePools {
    enabledFilters[httpFilterNameForInferencePool(pool)] = true
}

// Disable only filters NOT in the route's pool list
for key := range inferenceMatrix {
    if !enabledFilters[key] {
        route.TypedPerFilterConfig[key] = overrideAny
    }
}
```

**Critical Question:** With weighted clusters, which EPP should process the request?

**Answer:** Each weighted cluster targets a different EPP. The EPP filter runs BEFORE cluster selection. This means:
1. EPP-A processes request → sets destination header for pool-A
2. Envoy selects cluster based on weight → may route to pool-B cluster

This is **architecturally problematic**. The EPP selected by per-route config won't match the weighted cluster selection.

---

### Q4: Files with `len(inferencePools) != 1` Checks

**Complete list found:**

| File | Line | Code |
|------|------|------|
| `post_cluster_modify.go` | 49-51 | `if len(inferencePools) != 1 { return error }` |
| `post_route_modify.go` | 35-36 | `if len(inferencePools) != 1 { return error }` |

**Missing from original plan:** `post_route_modify.go:35-36` - This was noted but the plan didn't detail the fix.

---

### Q5: Cluster Metadata Propagation

**Current mechanism in `setClusterMetadataBackendName()` (lines 743-761):**

```go
func setClusterMetadataBackendName(cluster *clusterv3.Cluster, namespace, name, routeName string, routeRuleIndex, refIndex int) {
    // Sets filter metadata on cluster
    cluster.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace] = ...
    m.Fields[internalapi.InternalMetadataBackendNameKey] = structpb.NewStringValue(
        internalapi.PerRouteRuleRefBackendName(namespace, name, routeName, routeRuleIndex, refIndex),
    )
}
```

**For weighted clusters:**
- Each ORIGINAL_DST cluster gets its own metadata via `buildEPPMetadataForCluster()`
- Metadata includes: namespace, name, serviceName, port, bodyMode, allowModeOverride
- When creating multiple clusters for weighted routing, each needs proper metadata

**Required change:** Loop over backends and create separate clusters with appropriate indices.

---

### Q6: Ordering/Race Conditions

**Current flow:**

1. `PostClusterModify` - Called per-cluster by EG (can't create new clusters here)
2. `PostRouteModify` - Called per-route by EG (can't create new clusters here)
3. `PostTranslateModify` - Called ONCE with all clusters/routes (CAN modify everything)

**Order in PostTranslateModify:**
1. `maybeModifyCluster()` - Process existing clusters (adds metadata, filters)
2. `buildClustersForInferencePoolEndpointPickers()` - Add EPP clusters
3. `maybeModifyListenerAndRoutes()` - Add EPP filters to listeners, configure routes

**Potential issue:** `maybeModifyCluster()` runs before new clusters are created. If we create new weighted ORIGINAL_DST clusters, they need modification too.

**Solution:** Create new clusters BEFORE running `maybeModifyCluster()`, or run modification on new clusters explicitly.

---

## Architecture Gap Analysis

### The EPP Filter Selection Problem

**Critical Issue:** With weighted clusters, request flow is:

```
Request → EPP Filter (one selected by per-route config) → Weighted Cluster Selection → Endpoint
```

But we need:

```
Request → Weighted Cluster Selection → Corresponding EPP Filter → Endpoint
```

**The Problem:**
- EPP filter is selected at listener/route level BEFORE cluster routing
- Weighted cluster selection happens AFTER EPP processing
- EPP sets `x-gateway-destination-endpoint` header
- If EPP-A runs but cluster-B is selected, the destination header targets wrong pool

### Possible Solutions

**Solution A: Upstream EPP Filter (Recommended)**

Use the UPSTREAM ext_proc filter (already exists in `maybeModifyCluster`):

```go
// Already added per-cluster in maybeModifyCluster():
extProcFilter := &httpconnectionmanagerv3.HttpFilter{
    Name:       aiGatewayExtProcName,
    ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: ecAny},
}
po.HttpFilters = append(po.HttpFilters, extProcFilter, ...)
```

This filter runs AFTER cluster selection (at upstream level), not before. Each weighted cluster can have its own upstream EPP configuration.

**Solution B: Single EPP with Pool Awareness**

Pass all pool information to single EPP. EPP internally handles weighted selection. Requires EPP service modification.

---

## Revised Option C Implementation Plan

### Phase 1: API Layer (30 min) - NO CHANGE

```go
// Remove CEL validation
// api/v1alpha1/ai_gateway_route.go:203
// - message="only one InferencePool backend is allowed per rule"
```

### Phase 2: Remove Blocking Checks (1 hour)

**File: `post_cluster_modify.go:49-51`**
```go
// REMOVE:
if len(inferencePools) != 1 {
    return nil, fmt.Errorf("BUG: at most one inferencepool...")
}
// CHANGE TO:
if len(inferencePools) == 0 {
    return &egextension.PostClusterModifyResponse{Cluster: req.Cluster}, nil
}
// Handle first pool (EG sends one cluster per rule)
s.handleInferencePoolCluster(req.Cluster, inferencePools[0])
```

**File: `post_route_modify.go:35-36`**
```go
// REMOVE:
if len(inferencePools) != 1 {
    return nil, fmt.Errorf("BUG: at most one inferencepool...")
}
// CHANGE TO:
if len(inferencePools) > 0 {
    // Attach metadata for ALL pools (needed for weighted cluster generation)
    buildEPPMetadataForRoute(req.Route, inferencePools) // Modified to accept slice
}
```

### Phase 3: Generate Weighted Clusters (4-5 hours)

**New function in `post_translate_modify.go`:**

```go
func (s *Server) maybeCreateWeightedInferencePoolClusters(clusters []*clusterv3.Cluster, routes []*routev3.RouteConfiguration) ([]*clusterv3.Cluster, error) {
    var newClusters []*clusterv3.Cluster

    for _, routeCfg := range routes {
        for _, vh := range routeCfg.VirtualHosts {
            for _, route := range vh.Routes {
                pools := getInferencePoolsFromRouteMetadata(route.Metadata)
                if len(pools) <= 1 {
                    continue // No weighted routing needed
                }

                // Create separate ORIGINAL_DST cluster per pool
                for i, pool := range pools {
                    clusterName := weightedClusterNameForPool(route.Name, pool, i)
                    newCluster := buildWeightedInferencePoolCluster(pool, clusterName)
                    newClusters = append(newClusters, newCluster)
                }

                // Modify route to use weighted_clusters
                modifyRouteForWeightedClusters(route, pools)
            }
        }
    }
    return newClusters, nil
}
```

**Call site in PostTranslateModify:**
```go
// After existing cluster processing
weightedClusters, err := s.maybeCreateWeightedInferencePoolClusters(req.Clusters, req.Routes)
if err != nil {
    return nil, err
}
req.Clusters = append(req.Clusters, weightedClusters...)
```

### Phase 4: Handle EPP Filters (2-3 hours)

**Leverage existing UPSTREAM ext_proc pattern:**

The current code already adds ext_proc filters to clusters via `maybeModifyCluster()`. For weighted clusters:

1. Each new ORIGINAL_DST cluster for a weighted pool needs its own EPP config
2. Modify `buildWeightedInferencePoolCluster()` to include EPP cluster reference
3. Ensure `buildClustersForInferencePoolEndpointPickers()` creates EPP clusters for all pools

**File: `inferencepool.go` - new function:**
```go
func buildWeightedInferencePoolCluster(pool *gwaiev1.InferencePool, name string) *clusterv3.Cluster {
    cluster := &clusterv3.Cluster{
        Name:                 name,
        ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_ORIGINAL_DST},
        LbPolicy:             clusterv3.Cluster_CLUSTER_PROVIDED,
        ConnectTimeout:       durationpb.New(10 * time.Second),
        LbConfig: &clusterv3.Cluster_OriginalDstLbConfig_{
            OriginalDstLbConfig: &clusterv3.Cluster_OriginalDstLbConfig{
                UseHttpHeader:  true,
                HttpHeaderName: internalapi.EndpointPickerHeaderKey,
            },
        },
    }
    buildEPPMetadataForCluster(cluster, pool)
    return cluster
}
```

### Phase 5: Tests (2-3 hours)

1. Update CEL validation tests to expect success
2. Add unit test for weighted cluster generation
3. Add integration test for route modification
4. Add e2e test with traffic verification

---

## Risk Assessment

| Risk | Severity | Mitigation |
|------|----------|------------|
| EPP filter/cluster mismatch | HIGH | Use upstream ext_proc pattern |
| Cluster naming collisions | MEDIUM | Use unique naming scheme with route+index |
| Metadata propagation | MEDIUM | Test explicitly with multiple pools |
| Backward compatibility | LOW | Single pool case unchanged |

---

## Unresolved Questions

1. **Upstream ext_proc for EPP:** Does EPP work as upstream filter, or only as listener filter?
2. **Weight semantics:** Per-request or per-connection weighting?
3. **EPP service compatibility:** Do standard EPPs handle multiple simultaneous requests correctly?

---

## Conclusion

Option C is **feasible** but more complex than originally estimated due to the EPP filter selection problem. The solution using **upstream ext_proc filters** (already implemented for AI Gateway ext_proc) should work for EPP as well.

**Recommended approach:**
1. Start with API validation removal and blocking check removal
2. Implement weighted cluster generation in PostTranslateModify
3. Ensure each weighted cluster has proper upstream EPP configuration
4. Comprehensive testing before merge

**Total estimated effort:** 8-12 hours (vs original 8-11 hours)

---

*Generated by Claude Code Planning Agent*
