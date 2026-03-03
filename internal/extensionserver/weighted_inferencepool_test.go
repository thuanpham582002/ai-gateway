// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestWeightedPoolBackendRefIndex(t *testing.T) {
	// Test that WeightedPool correctly tracks BackendRefIndex
	t.Run("basic index tracking", func(t *testing.T) {
		pool := &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pool",
				Namespace: "test-ns",
			},
		}
		wp := WeightedPool{
			Pool:            pool,
			Weight:          80,
			BackendRefIndex: 2, // Third backend in backendRefs
		}

		require.Equal(t, 2, wp.BackendRefIndex)
		require.Equal(t, "test-pool", wp.Pool.Name)
		require.Equal(t, uint32(80), wp.Weight)
	})
}

func TestWeightedPoolBackendNameMatch(t *testing.T) {
	// Test that backend name generated from WeightedPool matches controller's format
	t.Run("backend name matches controller format", func(t *testing.T) {
		namespace := "myproject"
		poolName := "vllm-llama3-8b-v1"
		routeName := "llama3-canary"
		ruleIndex := 0
		backendRefIndex := 1 // Second backend in the rule

		// This is how the controller generates the backend name
		expectedBackendName := internalapi.PerRouteRuleRefBackendName(
			namespace, poolName, routeName, ruleIndex, backendRefIndex,
		)

		// Verify the format
		require.Equal(t, "myproject/vllm-llama3-8b-v1/route/llama3-canary/rule/0/ref/1", expectedBackendName)

		// Create a WeightedPool as it would be created during route processing
		wp := WeightedPool{
			Pool: &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      poolName,
					Namespace: namespace,
				},
			},
			Weight:          20,
			BackendRefIndex: backendRefIndex,
		}

		// Verify the pool was created correctly
		require.Equal(t, uint32(20), wp.Weight)

		// The backend name generated using wp.BackendRefIndex should match
		actualBackendName := internalapi.PerRouteRuleRefBackendName(
			namespace, wp.Pool.Name, routeName, ruleIndex, wp.BackendRefIndex,
		)
		require.Equal(t, expectedBackendName, actualBackendName)
	})

	t.Run("multiple pools preserve correct indices", func(t *testing.T) {
		namespace := "default"
		routeName := "weighted-route"
		ruleIndex := 0

		// Simulate a rule with 3 backends: AIServiceBackend at 0, InferencePool at 1, InferencePool at 2
		// Only InferencePools get into weightedPools, but they keep their original indices
		weightedPools := []WeightedPool{
			{
				Pool: &gwaiev1.InferencePool{
					ObjectMeta: metav1.ObjectMeta{Name: "pool-v1", Namespace: namespace},
				},
				Weight:          80,
				BackendRefIndex: 1, // Was at index 1 in original backendRefs
			},
			{
				Pool: &gwaiev1.InferencePool{
					ObjectMeta: metav1.ObjectMeta{Name: "pool-v2", Namespace: namespace},
				},
				Weight:          20,
				BackendRefIndex: 2, // Was at index 2 in original backendRefs
			},
		}

		// Verify each pool generates the correct backend name
		for _, wp := range weightedPools {
			expectedName := internalapi.PerRouteRuleRefBackendName(
				namespace, wp.Pool.Name, routeName, ruleIndex, wp.BackendRefIndex,
			)

			// For pool-v1: "default/pool-v1/route/weighted-route/rule/0/ref/1"
			// For pool-v2: "default/pool-v2/route/weighted-route/rule/0/ref/2"
			if wp.Pool.Name == "pool-v1" {
				require.Equal(t, "default/pool-v1/route/weighted-route/rule/0/ref/1", expectedName)
			} else {
				require.Equal(t, "default/pool-v2/route/weighted-route/rule/0/ref/2", expectedName)
			}
		}
	})
}

func TestWeightedPoolIndexFromBackendRefs(t *testing.T) {
	// Test simulating how getWeightedPoolsForRoute builds weightedPools with correct indices
	t.Run("extracts correct indices from backendRefs", func(t *testing.T) {
		// Simulate a rule with mixed backends
		backendRefs := []aigv1a1.AIGatewayRouteRuleBackendRef{
			{
				Name: "ai-service-backend", // Index 0 - not an InferencePool
			},
			{
				Name:   "pool-stable",
				Group:  ptr.To("inference.networking.k8s.io"),
				Kind:   ptr.To("InferencePool"),
				Weight: ptr.To(int32(80)),
			},
			{
				Name:   "pool-canary",
				Group:  ptr.To("inference.networking.k8s.io"),
				Kind:   ptr.To("InferencePool"),
				Weight: ptr.To(int32(20)),
			},
		}

		// Simulate the loop in getWeightedPoolsForRoute
		var weightedPools []WeightedPool
		for i, backendRef := range backendRefs {
			// Check if this is an InferencePool reference
			if backendRef.Group == nil || *backendRef.Group != "inference.networking.k8s.io" {
				continue
			}
			if backendRef.Kind == nil || *backendRef.Kind != "InferencePool" {
				continue
			}

			weight := uint32(1)
			if backendRef.Weight != nil && *backendRef.Weight > 0 {
				weight = uint32(*backendRef.Weight)
			}

			weightedPools = append(weightedPools, WeightedPool{
				Pool: &gwaiev1.InferencePool{
					ObjectMeta: metav1.ObjectMeta{Name: backendRef.Name, Namespace: "test-ns"},
				},
				Weight:          weight,
				BackendRefIndex: i, // Capture original index
			})
		}

		// Verify we got 2 pools with correct indices
		require.Len(t, weightedPools, 2)

		// First pool (pool-stable) was at index 1
		require.Equal(t, "pool-stable", weightedPools[0].Pool.Name)
		require.Equal(t, 1, weightedPools[0].BackendRefIndex)
		require.Equal(t, uint32(80), weightedPools[0].Weight)

		// Second pool (pool-canary) was at index 2
		require.Equal(t, "pool-canary", weightedPools[1].Pool.Name)
		require.Equal(t, 2, weightedPools[1].BackendRefIndex)
		require.Equal(t, uint32(20), weightedPools[1].Weight)
	})
}

func TestWeightedClusterNameForPool(t *testing.T) {
	t.Run("generates correct cluster name", func(t *testing.T) {
		pool := &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vllm-pool",
				Namespace: "inference",
			},
		}
		clusterName := weightedClusterNameForPool(pool)
		require.Equal(t, "ai-gateway-weighted-inferencepool-inference-vllm-pool", clusterName)
	})
}
