// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package events

import "encoding/json"

const (
	parseFailureMessageMaxBytes  = 1024
	parseFailureMaxToolTypes     = 64
	parseFailureToolTypeMaxBytes = 128
)

func buildParseFailureInfo(body []byte, message string) (*ParseFailureInfo, string, bool) {
	info := &ParseFailureInfo{
		Stage:   "request_body_parse",
		Message: truncateBytes(message, parseFailureMessageMaxBytes),
	}
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Tools  []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	if json.Unmarshal(body, &request) != nil {
		return info, "", false
	}
	for _, tool := range request.Tools {
		if len(info.ToolTypes) == parseFailureMaxToolTypes {
			info.ToolTypesTruncated = true
			break
		}
		info.ToolTypes = append(info.ToolTypes, truncateBytes(tool.Type, parseFailureToolTypeMaxBytes))
	}
	return info, request.Model, request.Stream
}

func truncateBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
