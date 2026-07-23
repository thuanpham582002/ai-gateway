// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	nativeIdentifierMaxBytes       = 1024
	nativeResponseMetadataMaxBytes = 64 * 1024
)

func (p *publisherState) extractNativeMetadata(body []byte) {
	correlation := &CorrelationInfo{
		ConversationID: nativeIdentifier(p.headers["x-conversation-id"]),
		BranchID:       nativeIdentifier(p.headers["x-branch-id"]),
		TurnID:         nativeIdentifier(p.headers["x-turn-id"]),
		ParentTurnID:   nativeIdentifier(p.headers["x-parent-turn-id"]),
		AgentID:        nativeIdentifier(p.headers["x-claude-code-agent-id"]),
		ParentAgentID:  nativeIdentifier(p.headers["x-claude-code-parent-agent-id"]),
	}
	if claudeSessionID := p.headers["x-claude-code-session-id"]; claudeSessionID != "" {
		correlation.SessionID = nativeIdentifier(claudeSessionID)
		p.client = &ClientInfo{Name: "claude_code"}
	}

	var envelope struct {
		PreviousResponseID string                     `json:"previous_response_id"`
		Conversation       json.RawMessage            `json:"conversation"`
		ClientMetadata     map[string]json.RawMessage `json:"client_metadata"`
	}
	if len(body) > 0 && json.Unmarshal(body, &envelope) == nil {
		correlation.PreviousResponseID = nativeIdentifier(envelope.PreviousResponseID)
		correlation.ConversationID = nativeIdentifier(responseConversationID(envelope.Conversation, correlation.ConversationID))
		if metadata, ok := envelope.ClientMetadata["x-codex-turn-metadata"]; ok {
			applyCodexMetadata(metadata, correlation)
			p.client = &ClientInfo{Name: "codex"}
		}
	}
	if metadata := p.headers["x-codex-turn-metadata"]; metadata != "" {
		applyCodexMetadata(json.RawMessage(metadata), correlation)
		p.client = &ClientInfo{Name: "codex"}
	}
	if correlation.WindowID == "" {
		correlation.WindowID = nativeIdentifier(p.headers["x-codex-window-id"])
	}
	if correlation.ParentThreadID == "" {
		correlation.ParentThreadID = nativeIdentifier(p.headers["x-codex-parent-thread-id"])
	}
	if p.client == nil {
		p.client = clientFromUserAgent(p.headers["user-agent"])
	}
	if *correlation != (CorrelationInfo{}) {
		p.correlation = correlation
	}
}

func responseConversationID(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback
	}
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	var conversation struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &conversation) == nil && conversation.ID != "" {
		return conversation.ID
	}
	return fallback
}

func applyCodexMetadata(raw json.RawMessage, correlation *CorrelationInfo) {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var metadata struct {
		SessionID          string `json:"session_id"`
		ThreadID           string `json:"thread_id"`
		ParentThreadID     string `json:"parent_thread_id"`
		TurnID             string `json:"turn_id"`
		WindowID           string `json:"window_id"`
		ForkedFromThreadID string `json:"forked_from_thread_id"`
	}
	if json.Unmarshal(raw, &metadata) != nil {
		return
	}
	if metadata.SessionID != "" {
		correlation.SessionID = nativeIdentifier(metadata.SessionID)
	}
	if metadata.ThreadID != "" {
		correlation.ThreadID = nativeIdentifier(metadata.ThreadID)
	}
	if metadata.ParentThreadID != "" {
		correlation.ParentThreadID = nativeIdentifier(metadata.ParentThreadID)
	}
	if metadata.TurnID != "" {
		correlation.TurnID = nativeIdentifier(metadata.TurnID)
	}
	if metadata.WindowID != "" {
		correlation.WindowID = nativeIdentifier(metadata.WindowID)
	}
	if metadata.ForkedFromThreadID != "" {
		correlation.ForkedFromThreadID = nativeIdentifier(metadata.ForkedFromThreadID)
	}
}

func (p *publisherState) observeNativeResponseMetadata(body []byte) {
	if correlationProviderResponseID(p.correlation) != "" {
		return
	}
	// Native response IDs are in the leading envelope. Keep this parser independent
	// of opt-in body capture; switch to an incremental JSON parser if that changes.
	remaining := nativeResponseMetadataMaxBytes - len(p.responseMetadata)
	if remaining <= 0 {
		return
	}
	if len(body) > remaining {
		body = body[:remaining]
	}
	p.responseMetadata = append(p.responseMetadata, body...)
	responseID := providerResponseIDFromBody(p.operation, p.responseMetadata)
	if responseID == "" {
		return
	}
	responseID = nativeIdentifier(responseID)
	if responseID == "" {
		return
	}
	if p.correlation == nil {
		p.correlation = &CorrelationInfo{}
	}
	p.correlation.ProviderResponseID = responseID
	if p.operation == "responses" || p.operation == "responses_compact" {
		p.correlation.ResponseID = responseID
	}
	p.responseMetadata = nil
}

func nativeIdentifier(value string) string {
	if len(value) > nativeIdentifierMaxBytes {
		return ""
	}
	return value
}

func providerResponseIDFromBody(operation string, body []byte) string {
	if responseID := gjson.GetBytes(body, "id"); responseID.Type == gjson.String {
		return responseID.String()
	}
	for line := range bytes.SplitSeq(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		paths := []string{"id"}
		switch operation {
		case "responses", "responses_compact":
			paths = append(paths, "response.id")
		case "messages":
			paths = append(paths, "message.id")
		}
		for _, path := range paths {
			if responseID := gjson.GetBytes(payload, path); responseID.Type == gjson.String {
				return responseID.String()
			}
		}
	}
	return ""
}

func correlationProviderResponseID(correlation *CorrelationInfo) string {
	if correlation == nil {
		return ""
	}
	return correlation.ProviderResponseID
}

func conversationKey(correlation *CorrelationInfo) string {
	if correlation == nil {
		return ""
	}
	switch {
	case correlation.ConversationID != "":
		return "conversation:" + correlation.ConversationID
	case correlation.SessionID != "" && correlation.ThreadID != "":
		return "session:" + correlation.SessionID + "/thread:" + correlation.ThreadID
	case correlation.SessionID != "":
		return "session:" + correlation.SessionID
	case correlation.ThreadID != "":
		return "thread:" + correlation.ThreadID
	default:
		return ""
	}
}

func clientFromUserAgent(userAgent string) *ClientInfo {
	userAgent = strings.ToLower(userAgent)
	clients := []struct {
		token string
		name  string
	}{
		{token: "codex", name: "codex"},
		{token: "claude", name: "claude_code"},
		{token: "opencode", name: "opencode"},
		{token: "curl/", name: "curl"},
	}
	for _, client := range clients {
		if strings.Contains(userAgent, client.token) {
			return &ClientInfo{Name: client.name}
		}
	}
	return nil
}
