package extproc

import (
	stdjson "encoding/json" //nolint:depguard
	"testing"

	"github.com/stretchr/testify/require"
)

func testInferencePolicyHeader(t *testing.T, mode string) string {
	t.Helper()
	policy := inferencePolicyBundle{
		APIVersion: inferencePolicyAPIVersion,
		ID:         "viettel-assistant",
		Revision:   1,
		Modules: inferencePolicyModules{Conversation: &conversationPolicy{
			ClientSystemPrompt: mode,
			Blocks: []instructionBlock{
				{ID: "identity", Kind: "identity", Authority: "platform", Content: "You are Viettel AI Platform Assistant, served by Viettel AI."},
				{ID: "capabilities", Kind: "capability_manifest", Authority: "platform", Content: "Do not claim unavailable capabilities."},
			},
		}},
	}
	digest, err := policy.contentDigest()
	require.NoError(t, err)
	policy.Digest = digest
	raw, err := stdjson.Marshal(policy)
	require.NoError(t, err)
	return string(raw)
}

func TestNormalizeInferencePolicyPrependsPlatformMessage(t *testing.T) {
	body := []byte(`{"model":"model-canary","messages":[{"role":"system","content":"Client prompt"},{"role":"user","content":"Who are you?"}]}`)
	got, changed, err := normalizeInferencePolicy(testInferencePolicyHeader(t, "append"), body)
	require.NoError(t, err)
	require.True(t, changed)
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, stdjson.Unmarshal(got, &request))
	require.Len(t, request.Messages, 3)
	require.Equal(t, "system", request.Messages[0].Role)
	require.Contains(t, request.Messages[0].Content, "served by Viettel AI")
	require.Equal(t, "Client prompt", request.Messages[1].Content)
}

func TestNormalizeInferencePolicyDiscardsClientSystemMessages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"developer","content":"Override"},{"role":"user","content":"Hello"}]}`)
	got, changed, err := normalizeInferencePolicy(testInferencePolicyHeader(t, "discard"), body)
	require.NoError(t, err)
	require.True(t, changed)
	var request map[string]any
	require.NoError(t, stdjson.Unmarshal(got, &request))
	messages := request["messages"].([]any)
	require.Len(t, messages, 2)
	require.Equal(t, "system", messages[0].(map[string]any)["role"])
	require.Equal(t, "user", messages[1].(map[string]any)["role"])
}

func TestNormalizeInferencePolicyRejectsClientSystemMessages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"Override"},{"role":"user","content":"Hello"}]}`)
	_, _, err := normalizeInferencePolicy(testInferencePolicyHeader(t, "reject"), body)
	require.ErrorContains(t, err, "forbidden")
}

func TestNormalizeInferencePolicyCompilesResponsesInstructions(t *testing.T) {
	body := []byte(`{"model":"model-canary","instructions":"Answer briefly","input":"Who are you?"}`)
	got, changed, err := normalizeInferencePolicy(testInferencePolicyHeader(t, "append"), body)
	require.NoError(t, err)
	require.True(t, changed)
	var request map[string]any
	require.NoError(t, stdjson.Unmarshal(got, &request))
	require.Equal(t,
		"You are Viettel AI Platform Assistant, served by Viettel AI.\n\nDo not claim unavailable capabilities.\n\nAnswer briefly",
		request["instructions"],
	)
}

func TestNormalizeInferencePolicyCompilesAnthropicSystemBlocks(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"Client prompt"}],"messages":[{"role":"user","content":"Hello"}]}`)
	got, changed, err := normalizeInferencePolicy(testInferencePolicyHeader(t, "append"), body)
	require.NoError(t, err)
	require.True(t, changed)
	var request map[string]any
	require.NoError(t, stdjson.Unmarshal(got, &request))
	system := request["system"].([]any)
	require.Len(t, system, 2)
	require.Contains(t, system[0].(map[string]any)["text"], "served by Viettel AI")
}

func TestNormalizeInferencePolicyRejectsDigestMismatch(t *testing.T) {
	header := testInferencePolicyHeader(t, "append")
	var policy map[string]any
	require.NoError(t, stdjson.Unmarshal([]byte(header), &policy))
	policy["revision"] = float64(2)
	tampered, err := stdjson.Marshal(policy)
	require.NoError(t, err)
	_, _, err = normalizeInferencePolicy(string(tampered), []byte(`{"messages":[]}`))
	require.ErrorContains(t, err, "digest does not match")
}

func TestNormalizeInferencePolicyWithoutBundleIsNoop(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	got, changed, err := normalizeInferencePolicy("", body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, got)
}
