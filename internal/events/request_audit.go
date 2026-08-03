// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/json"
	"strings"
)

const (
	requestAuditMaxBytes     = 256 * 1024
	requestAuditMaxToolCalls = 64
	toolArgumentMaxBytes     = 64 * 1024
)

var requestContentFields = map[string]bool{
	"contents":     true,
	"input":        true,
	"instructions": true,
	"messages":     true,
	"prompt":       true,
	"system":       true,
}

func buildRequestAudit(body []byte) *RequestAudit {
	if len(body) == 0 {
		return nil
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return nil
	}
	audit := &RequestAudit{Body: make(map[string]any, len(request))}
	for key, value := range request {
		if requestContentFields[strings.ToLower(key)] {
			collectToolCalls(value, audit)
			continue
		}
		audit.Body[key] = value
	}
	if len(audit.Body) == 0 {
		audit.Body = nil
	} else if encoded, err := json.Marshal(audit.Body); err != nil || len(encoded) > requestAuditMaxBytes {
		audit.Body = nil
		audit.Truncated = true
	}
	if len(audit.ToolCalls) == 0 && len(audit.Body) == 0 {
		return nil
	}
	return audit
}

func collectToolCalls(value any, audit *RequestAudit) {
	if audit.Truncated || len(audit.ToolCalls) >= requestAuditMaxToolCalls {
		audit.Truncated = true
		return
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectToolCalls(item, audit)
			if audit.Truncated {
				return
			}
		}
	case map[string]any:
		if calls, ok := typed["tool_calls"]; ok {
			collectToolCallList(calls, audit)
		}
		if call, ok := typed["function_call"]; ok {
			collectSingleToolCall(call, "function", audit)
		}
		if call, ok := typed["functionCall"]; ok {
			collectSingleToolCall(call, "function", audit)
		}
		typeName, _ := typed["type"].(string)
		switch typeName {
		case "function_call":
			appendToolCall(typed, "function", typed["arguments"], audit)
		case "tool_use":
			appendToolCall(typed, "tool_use", typed["input"], audit)
		}
		for key, child := range typed {
			switch key {
			case "content", "parts":
				collectToolCalls(child, audit)
			}
		}
	}
}

func collectToolCallList(value any, audit *RequestAudit) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range items {
		collectSingleToolCall(item, "function", audit)
		if audit.Truncated {
			return
		}
	}
}

func collectSingleToolCall(value any, defaultType string, audit *RequestAudit) {
	call, ok := value.(map[string]any)
	if !ok {
		return
	}
	if function, ok := call["function"].(map[string]any); ok {
		merged := map[string]any{"id": call["id"], "type": call["type"], "name": function["name"]}
		appendToolCall(merged, defaultType, function["arguments"], audit)
		return
	}
	appendToolCall(call, defaultType, call["arguments"], audit)
}

func appendToolCall(call map[string]any, defaultType string, arguments any, audit *RequestAudit) {
	if len(audit.ToolCalls) >= requestAuditMaxToolCalls {
		audit.Truncated = true
		return
	}
	name, _ := call["name"].(string)
	if name == "" {
		return
	}
	id, _ := call["id"].(string)
	if id == "" {
		id, _ = call["call_id"].(string)
	}
	typeName, _ := call["type"].(string)
	if typeName == "" {
		typeName = defaultType
	}
	encoded := encodeToolArguments(arguments)
	remaining := requestAuditMaxBytes - audit.argumentBytes
	if remaining <= 0 {
		audit.Truncated = true
		return
	}
	limit := toolArgumentMaxBytes
	if remaining < limit {
		limit = remaining
	}
	if len(encoded) > limit {
		encoded = encoded[:limit]
		audit.Truncated = true
	}
	audit.argumentBytes += len(encoded)
	audit.ToolCalls = append(audit.ToolCalls, ToolCallAudit{
		ID: id, Type: typeName, Name: name, Arguments: encoded,
	})
}

func encodeToolArguments(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
