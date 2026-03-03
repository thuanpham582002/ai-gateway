// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// weightedInferencePoolClusterPrefix is the prefix for weighted inference pool clusters.
const weightedInferencePoolClusterPrefix = "ai-gateway-weighted-inferencepool-"

// WeightedPool represents an InferencePool with its routing weight.
type WeightedPool struct {
	Pool   *gwaiev1.InferencePool
	Weight uint32
}

// maybeCreateWeightedInferencePoolClusters scans routes for multiple InferencePool backends
// and creates separate ORIGINAL_DST clusters for each pool with weighted routing.
// This is called from PostTranslateModify after the initial cluster/route generation.
func (s *Server) maybeCreateWeightedInferencePoolClusters(
	routes []*routev3.RouteConfiguration,
	clusters []*clusterv3.Cluster,
) ([]*clusterv3.Cluster, error) {
	var newClusters []*clusterv3.Cluster
	clusterExists := make(map[string]bool)

	// Mark existing clusters
	for _, c := range clusters {
		clusterExists[c.Name] = true
	}

	// Process each route configuration
	for _, routeCfg := range routes {
		for _, vh := range routeCfg.VirtualHosts {
			for _, route := range vh.Routes {
				s.log.V(1).Info("checking route for weighted InferencePools", "route", route.Name)

				// Check if this route has multiple InferencePools
				result, err := s.getWeightedPoolsForRoute(route)
				if err != nil {
					s.log.Error(err, "failed to get weighted pools for route", "route", route.Name)
					continue
				}

				if result == nil || len(result.Pools) <= 1 {
					// Single or no InferencePool, skip weighted cluster creation
					continue
				}

				s.log.Info("creating weighted clusters for route with multiple InferencePools",
					"route", route.Name, "pool_count", len(result.Pools))

				// Create ORIGINAL_DST cluster for each pool
				for i, wp := range result.Pools {
					clusterName := weightedClusterNameForPool(wp.Pool)
					if clusterExists[clusterName] {
						continue
					}

					cluster := s.createOriginalDstClusterForPool(wp.Pool, clusterName)

					// Set backend name metadata for the upstream ext_proc filter.
					// This is required for the AI Gateway ext_proc to identify the backend.
					setClusterMetadataBackendName(cluster, result.Route.Namespace, wp.Pool.Name,
						result.Route.Name, result.RuleIndex, i)

					newClusters = append(newClusters, cluster)
					clusterExists[clusterName] = true

					s.log.Info("created weighted InferencePool cluster",
						"cluster", clusterName, "pool", wp.Pool.Name, "weight", wp.Weight)
				}

				// Modify the route to use weighted_clusters
				if err := s.modifyRouteToWeightedClusters(route, result.Pools); err != nil {
					s.log.Error(err, "failed to modify route to weighted clusters", "route", route.Name)
					continue
				}
			}
		}
	}

	return newClusters, nil
}

// weightedPoolsResult contains the result of getWeightedPoolsForRoute.
type weightedPoolsResult struct {
	Pools     []WeightedPool
	Route     *aigv1a1.AIGatewayRoute
	RuleIndex int
}

// getWeightedPoolsForRoute extracts InferencePool references and their weights from a route.
func (s *Server) getWeightedPoolsForRoute(route *routev3.Route) (*weightedPoolsResult, error) {
	// Get the route's associated AIGatewayRoute to find backend weights
	// The route name follows the pattern: "httproute/<namespace>/<name>/rule/<rule_index>/match/<match_index>/*"
	aigwRoute, httpRouteRuleIndex, err := s.getAIGatewayRouteFromRouteName(route.Name)
	if err != nil {
		return nil, nil // Not an AIGatewayRoute route, skip
	}

	if httpRouteRuleIndex >= len(aigwRoute.Spec.Rules) {
		return nil, nil
	}

	rule := &aigwRoute.Spec.Rules[httpRouteRuleIndex]

	// Check if this rule has InferencePool backends
	var weightedPools []WeightedPool
	for _, backendRef := range rule.BackendRefs {
		// Check if this is an InferencePool reference
		if backendRef.Group == nil || *backendRef.Group != "inference.networking.k8s.io" {
			continue
		}
		if backendRef.Kind == nil || *backendRef.Kind != "InferencePool" {
			continue
		}

		// Get the InferencePool object
		namespace := aigwRoute.Namespace
		if backendRef.Namespace != nil {
			namespace = string(*backendRef.Namespace)
		}

		var pool gwaiev1.InferencePool
		if err := s.k8sClient.Get(context.Background(),
			client.ObjectKey{Namespace: namespace, Name: backendRef.Name}, &pool); err != nil {
			if apierrors.IsNotFound(err) {
				s.log.Info("InferencePool not found", "namespace", namespace, "name", backendRef.Name)
				continue
			}
			return nil, fmt.Errorf("failed to get InferencePool %s/%s: %w", namespace, backendRef.Name, err)
		}

		weight := uint32(1)
		if backendRef.Weight != nil && *backendRef.Weight > 0 {
			weight = uint32(*backendRef.Weight)
		}

		weightedPools = append(weightedPools, WeightedPool{
			Pool:   &pool,
			Weight: weight,
		})
	}

	return &weightedPoolsResult{
		Pools:     weightedPools,
		Route:     aigwRoute,
		RuleIndex: httpRouteRuleIndex,
	}, nil
}

// getAIGatewayRouteFromRouteName extracts the AIGatewayRoute and rule index from a route name.
// Route names follow the pattern: "httproute/<namespace>/<name>/rule/<rule_index>/match/<match_index>/*"
func (s *Server) getAIGatewayRouteFromRouteName(routeName string) (*aigv1a1.AIGatewayRoute, int, error) {
	// Parse route name to extract namespace, name, and rule index
	// Example: "httproute/testproject/route-ep-631844dd/rule/0/match/0/*"
	parts := strings.Split(routeName, "/")
	if len(parts) < 5 || parts[0] != "httproute" {
		return nil, 0, fmt.Errorf("invalid route name format: %s", routeName)
	}

	namespace := parts[1]
	name := parts[2]
	ruleIndex := 0

	// Find rule index - look for "rule" followed by a number
	for i, part := range parts {
		if part == "rule" && i+1 < len(parts) {
			if idx, err := strconv.Atoi(parts[i+1]); err == nil {
				ruleIndex = idx
				break
			}
		}
	}

	var aigwRoute aigv1a1.AIGatewayRoute
	if err := s.k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: namespace, Name: name}, &aigwRoute); err != nil {
		return nil, 0, err
	}

	return &aigwRoute, ruleIndex, nil
}

// createOriginalDstClusterForPool creates an ORIGINAL_DST cluster for a single InferencePool.
func (s *Server) createOriginalDstClusterForPool(pool *gwaiev1.InferencePool, clusterName string) *clusterv3.Cluster {
	cluster := &clusterv3.Cluster{
		Name:                 clusterName,
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_ORIGINAL_DST},
		LbPolicy:             clusterv3.Cluster_CLUSTER_PROVIDED,
		ConnectTimeout:       durationpb.New(10 * time.Second),
		LbConfig: &clusterv3.Cluster_OriginalDstLbConfig_{
			OriginalDstLbConfig: &clusterv3.Cluster_OriginalDstLbConfig{
				UseHttpHeader:  true,
				HttpHeaderName: internalapi.EndpointPickerHeaderKey,
			},
		},
		LoadBalancingPolicy: nil,
		EdsClusterConfig:    nil,
	}

	// Add InferencePool metadata to the cluster
	buildEPPMetadataForCluster(cluster, pool)

	return cluster
}

// modifyRouteToWeightedClusters modifies the route action to use weighted_clusters.
func (s *Server) modifyRouteToWeightedClusters(route *routev3.Route, weightedPools []WeightedPool) error {
	routeAction := route.GetRoute()
	if routeAction == nil {
		return fmt.Errorf("route %s has no route action", route.Name)
	}

	// Calculate total weight for normalization
	totalWeight := uint32(0)
	for _, wp := range weightedPools {
		totalWeight += wp.Weight
	}

	// Build weighted cluster configuration
	var clusterWeights []*routev3.WeightedCluster_ClusterWeight
	for _, wp := range weightedPools {
		clusterName := weightedClusterNameForPool(wp.Pool)
		clusterWeight := &routev3.WeightedCluster_ClusterWeight{
			Name:   clusterName,
			Weight: wrapperspb.UInt32(wp.Weight),
		}

		// Add metadata for the pool so ExtProc can identify which pool was selected
		clusterWeight.RequestHeadersToAdd = []*corev3.HeaderValueOption{
			{
				Header: &corev3.HeaderValue{
					Key:   internalapi.SelectedPoolHeader,
					Value: wp.Pool.Name,
				},
			},
		}

		clusterWeights = append(clusterWeights, clusterWeight)
	}

	// Set weighted_clusters as the route action
	routeAction.ClusterSpecifier = &routev3.RouteAction_WeightedClusters{
		WeightedClusters: &routev3.WeightedCluster{
			Clusters:    clusterWeights,
			TotalWeight: wrapperspb.UInt32(totalWeight),
		},
	}

	// Add session affinity metadata to route
	s.addSessionAffinityMetadata(route, weightedPools)

	return nil
}

// addSessionAffinityMetadata adds session affinity configuration to the route metadata.
// This allows ExtProc to read the configuration and apply session affinity logic.
func (s *Server) addSessionAffinityMetadata(route *routev3.Route, weightedPools []WeightedPool) {
	if route.Metadata == nil {
		route.Metadata = &corev3.Metadata{}
	}
	if route.Metadata.FilterMetadata == nil {
		route.Metadata.FilterMetadata = make(map[string]*structpb.Struct)
	}

	// Get or create the AI Gateway metadata namespace
	m, ok := route.Metadata.FilterMetadata[aigv1a1.AIGatewayFilterMetadataNamespace]
	if !ok {
		m = &structpb.Struct{}
		route.Metadata.FilterMetadata[aigv1a1.AIGatewayFilterMetadataNamespace] = m
	}
	if m.Fields == nil {
		m.Fields = make(map[string]*structpb.Value)
	}

	// Store pool information as metadata
	var poolList []*structpb.Value
	for _, wp := range weightedPools {
		poolInfo := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"name":      structpb.NewStringValue(wp.Pool.Name),
				"namespace": structpb.NewStringValue(wp.Pool.Namespace),
				"weight":    structpb.NewNumberValue(float64(wp.Weight)),
			},
		}
		poolList = append(poolList, structpb.NewStructValue(poolInfo))
	}

	m.Fields["weighted_inference_pools"] = structpb.NewListValue(&structpb.ListValue{Values: poolList})
}

// weightedClusterNameForPool returns the cluster name for a weighted InferencePool.
func weightedClusterNameForPool(pool *gwaiev1.InferencePool) string {
	return fmt.Sprintf("%s%s-%s", weightedInferencePoolClusterPrefix, pool.Namespace, pool.Name)
}
