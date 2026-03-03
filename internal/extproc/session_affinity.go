// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net/url"
	"strings"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
)

// PoolWithWeight represents an InferencePool backend with its weight for weighted routing.
type PoolWithWeight struct {
	// Name is the name of the InferencePool.
	Name string
	// Namespace is the namespace of the InferencePool.
	Namespace string
	// Weight is the traffic weight (0-100).
	Weight int32
}

// SelectPoolWithAffinity selects a pool using session affinity based on consistent hashing.
// It extracts a hash key from the request based on the configuration and uses it to
// deterministically select a pool. If no hash key is found, it applies the fallback behavior.
//
// Parameters:
//   - config: The session affinity configuration specifying where to extract hash keys from
//   - pools: List of pools with their weights
//   - headers: HTTP request headers as a map
//   - body: Request body (may be nil if not available)
//   - queryParams: Query parameters extracted from the URL
//
// Returns:
//   - Selected pool name
//   - Error if RejectRequest fallback is configured and no hash key is found
func SelectPoolWithAffinity(
	config *aigv1a1.SessionAffinityConfig,
	pools []PoolWithWeight,
	headers map[string]string,
	body []byte,
	queryParams url.Values,
) (string, error) {
	if len(pools) == 0 {
		return "", fmt.Errorf("no pools available for selection")
	}

	if len(pools) == 1 {
		return pools[0].Name, nil
	}

	// If no config or no hash sources, use weighted random selection
	if config == nil || len(config.HashOn) == 0 {
		return weightedRandomSelect(pools), nil
	}

	// Try to extract hash key from configured sources (priority order)
	var hashKey string
	for _, source := range config.HashOn {
		switch source.Type {
		case aigv1a1.HashSourceHeader:
			hashKey = getHeaderValue(headers, source.Name)
		case aigv1a1.HashSourceRequestBody:
			hashKey = extractJSONPath(body, source.JSONPath)
		case aigv1a1.HashSourceQueryParam:
			hashKey = getQueryParamValue(queryParams, source.Name)
		}
		if hashKey != "" {
			break // Found a hash key, stop searching
		}
	}

	// If hash key found, use consistent hashing
	if hashKey != "" {
		return consistentHashPool(hashKey, pools), nil
	}

	// No hash key found, apply fallback behavior
	switch config.Fallback {
	case aigv1a1.FallbackWeightedRandom:
		return weightedRandomSelect(pools), nil
	case aigv1a1.FallbackFirstBackend:
		return pools[0].Name, nil
	case aigv1a1.FallbackRejectRequest:
		return "", fmt.Errorf("session affinity required but no hash key found in request")
	default:
		// Default to weighted random if fallback is not specified
		return weightedRandomSelect(pools), nil
	}
}

// consistentHashPool returns deterministic pool selection based on hash key.
// Same key always maps to the same pool (stateless, no storage needed).
// The hash value is mapped to pools based on their cumulative weights.
func consistentHashPool(key string, pools []PoolWithWeight) string {
	// Normalize weights to ensure they sum to 100
	totalWeight := int32(0)
	for _, pool := range pools {
		totalWeight += pool.Weight
	}

	// Use FNV-1a hash for good distribution
	h := fnv.New32a()
	h.Write([]byte(key))
	hashValue := h.Sum32() % uint32(totalWeight)

	// Map hash to pool based on cumulative weights
	cumulative := uint32(0)
	for _, pool := range pools {
		cumulative += uint32(pool.Weight)
		if hashValue < cumulative {
			return pool.Name
		}
	}

	// Fallback (shouldn't reach here if weights sum correctly)
	return pools[0].Name
}

// weightedRandomSelect performs random weighted selection among pools.
func weightedRandomSelect(pools []PoolWithWeight) string {
	// Calculate total weight
	totalWeight := int32(0)
	for _, pool := range pools {
		totalWeight += pool.Weight
	}

	if totalWeight == 0 {
		// If all weights are 0, select first pool
		return pools[0].Name
	}

	// Generate random value in range [0, totalWeight)
	randomValue := rand.Int32N(totalWeight)

	// Find the pool corresponding to the random value
	cumulative := int32(0)
	for _, pool := range pools {
		cumulative += pool.Weight
		if randomValue < cumulative {
			return pool.Name
		}
	}

	// Fallback (shouldn't reach here)
	return pools[0].Name
}

// getHeaderValue extracts a header value by name (case-insensitive).
func getHeaderValue(headers map[string]string, name string) string {
	// HTTP headers are case-insensitive, try both cases
	if v, ok := headers[name]; ok {
		return v
	}
	// Try lowercase
	lowerName := strings.ToLower(name)
	for k, v := range headers {
		if strings.ToLower(k) == lowerName {
			return v
		}
	}
	return ""
}

// getQueryParamValue extracts a query parameter value by name.
func getQueryParamValue(params url.Values, name string) string {
	return params.Get(name)
}

// extractJSONPath extracts a value from JSON body using a simplified JSONPath.
// Supports simple paths like "$.user", "$.metadata.session_id".
// Returns empty string if the path doesn't exist or body is not valid JSON.
func extractJSONPath(body []byte, jsonPath string) string {
	if len(body) == 0 || jsonPath == "" {
		return ""
	}

	// Parse JSON into a map
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	// Handle JSONPath: remove leading "$." if present
	path := strings.TrimPrefix(jsonPath, "$.")
	if path == "" {
		return ""
	}

	// Split path into parts
	parts := strings.Split(path, ".")

	// Navigate through the JSON structure
	var current any = data
	for _, part := range parts {
		if m, ok := current.(map[string]any); ok {
			current = m[part]
		} else {
			return ""
		}
	}

	// Convert result to string
	switch v := current.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%v", v)
	case bool:
		return fmt.Sprintf("%v", v)
	case nil:
		return ""
	default:
		// For complex types, marshal back to JSON
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return ""
	}
}

// ParseQueryParams extracts query parameters from a URL path.
func ParseQueryParams(path string) url.Values {
	idx := strings.Index(path, "?")
	if idx == -1 {
		return url.Values{}
	}
	params, err := url.ParseQuery(path[idx+1:])
	if err != nil {
		return url.Values{}
	}
	return params
}
