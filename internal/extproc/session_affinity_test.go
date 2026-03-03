// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
)

func TestConsistentHashPool(t *testing.T) {
	pools := []PoolWithWeight{
		{Name: "pool-v1", Namespace: "default", Weight: 80},
		{Name: "pool-v2", Namespace: "default", Weight: 20},
	}

	// Test determinism: same key should always return same pool
	for i := 0; i < 100; i++ {
		result1 := consistentHashPool("user-123", pools)
		result2 := consistentHashPool("user-123", pools)
		require.Equal(t, result1, result2, "consistent hash should be deterministic")
	}

	// Test different keys produce (potentially) different results
	results := make(map[string]int)
	for i := 0; i < 1000; i++ {
		key := "user-" + string(rune('a'+i%26)) + string(rune('0'+i%10))
		result := consistentHashPool(key, pools)
		results[result]++
	}

	// Both pools should be selected (weighted distribution)
	require.Contains(t, results, "pool-v1")
	require.Contains(t, results, "pool-v2")

	// pool-v1 (80%) should be selected more often than pool-v2 (20%)
	// With 1000 samples, we expect roughly 800 vs 200
	require.Greater(t, results["pool-v1"], results["pool-v2"])
}

func TestWeightedRandomSelect(t *testing.T) {
	pools := []PoolWithWeight{
		{Name: "pool-v1", Namespace: "default", Weight: 80},
		{Name: "pool-v2", Namespace: "default", Weight: 20},
	}

	results := make(map[string]int)
	for i := 0; i < 10000; i++ {
		result := weightedRandomSelect(pools)
		results[result]++
	}

	// Both pools should be selected
	require.Contains(t, results, "pool-v1")
	require.Contains(t, results, "pool-v2")

	// pool-v1 (80%) should be selected roughly 4x more than pool-v2 (20%)
	ratio := float64(results["pool-v1"]) / float64(results["pool-v2"])
	require.InDelta(t, 4.0, ratio, 1.0, "ratio should be approximately 4:1")
}

func TestExtractJSONPath(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		jsonPath string
		expected string
	}{
		{
			name:     "simple string field",
			body:     []byte(`{"user": "user-123"}`),
			jsonPath: "$.user",
			expected: "user-123",
		},
		{
			name:     "nested field",
			body:     []byte(`{"metadata": {"session_id": "sess-456"}}`),
			jsonPath: "$.metadata.session_id",
			expected: "sess-456",
		},
		{
			name:     "number field",
			body:     []byte(`{"count": 42}`),
			jsonPath: "$.count",
			expected: "42",
		},
		{
			name:     "boolean field",
			body:     []byte(`{"active": true}`),
			jsonPath: "$.active",
			expected: "true",
		},
		{
			name:     "missing field",
			body:     []byte(`{"user": "user-123"}`),
			jsonPath: "$.session",
			expected: "",
		},
		{
			name:     "invalid json",
			body:     []byte(`{invalid}`),
			jsonPath: "$.user",
			expected: "",
		},
		{
			name:     "empty body",
			body:     []byte{},
			jsonPath: "$.user",
			expected: "",
		},
		{
			name:     "path without dollar sign",
			body:     []byte(`{"user": "user-123"}`),
			jsonPath: "user",
			expected: "user-123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractJSONPath(tc.body, tc.jsonPath)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestGetHeaderValue(t *testing.T) {
	headers := map[string]string{
		"X-Session-ID": "session-123",
		"x-user-id":    "user-456",
		"Content-Type": "application/json",
	}

	// Exact match
	require.Equal(t, "session-123", getHeaderValue(headers, "X-Session-ID"))

	// Case-insensitive match
	require.Equal(t, "session-123", getHeaderValue(headers, "x-session-id"))
	require.Equal(t, "user-456", getHeaderValue(headers, "X-User-ID"))

	// Missing header
	require.Equal(t, "", getHeaderValue(headers, "X-Missing"))
}

func TestSelectPoolWithAffinity(t *testing.T) {
	pools := []PoolWithWeight{
		{Name: "pool-v1", Namespace: "default", Weight: 80},
		{Name: "pool-v2", Namespace: "default", Weight: 20},
	}

	t.Run("with header hash source", func(t *testing.T) {
		config := &aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{
				{Type: aigv1a1.HashSourceHeader, Name: "X-Session-ID"},
			},
			Fallback: aigv1a1.FallbackWeightedRandom,
		}
		headers := map[string]string{"X-Session-ID": "session-123"}

		result1, err := SelectPoolWithAffinity(config, pools, headers, nil, nil)
		require.NoError(t, err)

		// Same session should always select same pool
		for i := 0; i < 10; i++ {
			result2, err := SelectPoolWithAffinity(config, pools, headers, nil, nil)
			require.NoError(t, err)
			require.Equal(t, result1, result2)
		}
	})

	t.Run("with request body hash source", func(t *testing.T) {
		config := &aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{
				{Type: aigv1a1.HashSourceRequestBody, JSONPath: "$.user"},
			},
			Fallback: aigv1a1.FallbackWeightedRandom,
		}
		body := []byte(`{"user": "user-123", "model": "gpt-4"}`)

		result1, err := SelectPoolWithAffinity(config, pools, nil, body, nil)
		require.NoError(t, err)

		// Same user should always select same pool
		for i := 0; i < 10; i++ {
			result2, err := SelectPoolWithAffinity(config, pools, nil, body, nil)
			require.NoError(t, err)
			require.Equal(t, result1, result2)
		}
	})

	t.Run("with query param hash source", func(t *testing.T) {
		config := &aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{
				{Type: aigv1a1.HashSourceQueryParam, Name: "session_id"},
			},
			Fallback: aigv1a1.FallbackWeightedRandom,
		}
		queryParams := url.Values{"session_id": []string{"sess-789"}}

		result1, err := SelectPoolWithAffinity(config, pools, nil, nil, queryParams)
		require.NoError(t, err)

		// Same session should always select same pool
		for i := 0; i < 10; i++ {
			result2, err := SelectPoolWithAffinity(config, pools, nil, nil, queryParams)
			require.NoError(t, err)
			require.Equal(t, result1, result2)
		}
	})

	t.Run("fallback to weighted random", func(t *testing.T) {
		config := &aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{
				{Type: aigv1a1.HashSourceHeader, Name: "X-Session-ID"},
			},
			Fallback: aigv1a1.FallbackWeightedRandom,
		}
		// No X-Session-ID header provided
		headers := map[string]string{}

		result, err := SelectPoolWithAffinity(config, pools, headers, nil, nil)
		require.NoError(t, err)
		require.Contains(t, []string{"pool-v1", "pool-v2"}, result)
	})

	t.Run("fallback to first backend", func(t *testing.T) {
		config := &aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{
				{Type: aigv1a1.HashSourceHeader, Name: "X-Session-ID"},
			},
			Fallback: aigv1a1.FallbackFirstBackend,
		}
		// No X-Session-ID header provided
		headers := map[string]string{}

		result, err := SelectPoolWithAffinity(config, pools, headers, nil, nil)
		require.NoError(t, err)
		require.Equal(t, "pool-v1", result)
	})

	t.Run("fallback reject request", func(t *testing.T) {
		config := &aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{
				{Type: aigv1a1.HashSourceHeader, Name: "X-Session-ID"},
			},
			Fallback: aigv1a1.FallbackRejectRequest,
		}
		// No X-Session-ID header provided
		headers := map[string]string{}

		_, err := SelectPoolWithAffinity(config, pools, headers, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "session affinity required")
	})

	t.Run("priority order of hash sources", func(t *testing.T) {
		config := &aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{
				{Type: aigv1a1.HashSourceHeader, Name: "X-Session-ID"},      // Priority 1
				{Type: aigv1a1.HashSourceRequestBody, JSONPath: "$.user"},   // Priority 2
				{Type: aigv1a1.HashSourceQueryParam, Name: "session_id"},    // Priority 3
			},
			Fallback: aigv1a1.FallbackWeightedRandom,
		}

		// When header is present, use it
		headers := map[string]string{"X-Session-ID": "header-session"}
		body := []byte(`{"user": "body-user"}`)
		queryParams := url.Values{"session_id": []string{"query-session"}}

		result1, err := SelectPoolWithAffinity(config, pools, headers, body, queryParams)
		require.NoError(t, err)

		// Result should be same as using only header
		headerOnlyResult, _ := SelectPoolWithAffinity(&aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{{Type: aigv1a1.HashSourceHeader, Name: "X-Session-ID"}},
		}, pools, headers, nil, nil)
		require.Equal(t, headerOnlyResult, result1)

		// When header is missing, fall back to body
		headers2 := map[string]string{}
		result2, err := SelectPoolWithAffinity(config, pools, headers2, body, queryParams)
		require.NoError(t, err)

		// Result should be same as using only body
		bodyOnlyResult, _ := SelectPoolWithAffinity(&aigv1a1.SessionAffinityConfig{
			HashOn: []aigv1a1.HashSource{{Type: aigv1a1.HashSourceRequestBody, JSONPath: "$.user"}},
		}, pools, nil, body, nil)
		require.Equal(t, bodyOnlyResult, result2)
	})

	t.Run("single pool returns immediately", func(t *testing.T) {
		singlePool := []PoolWithWeight{
			{Name: "only-pool", Namespace: "default", Weight: 100},
		}

		result, err := SelectPoolWithAffinity(nil, singlePool, nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, "only-pool", result)
	})

	t.Run("no pools returns error", func(t *testing.T) {
		_, err := SelectPoolWithAffinity(nil, []PoolWithWeight{}, nil, nil, nil)
		require.Error(t, err)
	})
}

func TestParseQueryParams(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected url.Values
	}{
		{
			name:     "with query params",
			path:     "/v1/completions?session_id=123&user=test",
			expected: url.Values{"session_id": []string{"123"}, "user": []string{"test"}},
		},
		{
			name:     "no query params",
			path:     "/v1/completions",
			expected: url.Values{},
		},
		{
			name:     "empty query string",
			path:     "/v1/completions?",
			expected: url.Values{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ParseQueryParams(tc.path)
			require.Equal(t, tc.expected, result)
		})
	}
}
