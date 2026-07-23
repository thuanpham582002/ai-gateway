// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import "strconv"

func billingInfo(headers map[string]string) *BillingInfo {
	if len(headers) == 0 {
		return nil
	}
	billing := &BillingInfo{
		APIKeyID:           headers["x-api-key-id"],
		BillingRequestID:   headers["x-maas-billing-request-id"],
		TenantID:           headers["x-tenant-id"],
		SubjectID:          headers["x-subject-id"],
		SubjectType:        headers["x-subject-type"],
		ProjectID:          headers["x-project-id"],
		InputPrice:         headers["x-input-price"],
		OutputPrice:        headers["x-output-price"],
		ReservedCost:       headers["x-maas-reserved-cost"],
		CreditRemaining:    headers["x-credit-remaining"],
		RateLimitRemaining: headers["x-rate-limit-remaining"],
		IsFree:             parseBool(headers["x-is-free"]),
		AdmissionReserved:  parseBool(headers["x-maas-admission-reserved"]),
		ReservedInputTokens: parseUint64(
			headers["x-maas-reserved-input-tokens"],
		),
		ReservedOutputTokens: parseUint64(
			headers["x-maas-reserved-output-tokens"],
		),
		ProjectInFlightSlot:  parseInt64(headers["x-maas-project-in-flight-slot"]),
		APIKeyInFlightSlot:   parseInt64(headers["x-maas-api-key-in-flight-slot"]),
		ModelConcurrencySlot: parseInt64(headers["x-maas-model-concurrency-slot"]),
	}
	if *billing == (BillingInfo{}) {
		return nil
	}
	return billing
}

func capturePolicy(maxInlineBytes int, bodyStore BodyStore) CapturePolicy {
	mode := "hash_only"
	if maxInlineBytes > 0 {
		mode = "bounded_inline"
	}
	policy := CapturePolicy{Mode: mode, MaxInlineBytes: maxInlineBytes}
	if bodyStore != nil {
		policy.ExternalBodyStore = &ExternalStorePolicy{
			Provider:     "s3",
			MaxBodyBytes: bodyStore.MaxBodyBytes(),
		}
	}
	return policy
}

func parseBool(value string) *bool {
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseUint64(value string) *uint64 {
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseInt64(value string) *int64 {
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}
