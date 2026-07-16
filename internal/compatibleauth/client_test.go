// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache License 2.0 is available at the root of this repository.

package compatibleauth

import (
	"context"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type fakeAuthClient struct {
	request  *authv3.CheckRequest
	response *authv3.CheckResponse
}

func (f *fakeAuthClient) Check(_ context.Context, request *authv3.CheckRequest, _ ...grpc.CallOption) (*authv3.CheckResponse, error) {
	f.request = request
	return f.response, nil
}

func TestAuthorizeScopesPathAndPreservesDeniedResponse(t *testing.T) {
	fake := &fakeAuthClient{response: &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.PermissionDenied)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{DeniedResponse: &authv3.DeniedHttpResponse{
			Status: &typev3.HttpStatus{Code: typev3.StatusCode(503)},
			Headers: []*corev3.HeaderValueOption{
				{Header: &corev3.HeaderValue{Key: "content-type", Value: "application/json"}},
				{Header: &corev3.HeaderValue{Key: "retry-after", Value: "30"}},
			},
			Body: `{"error":{"code":"DEPLOYMENT_SCALING_UP"}}`,
		}},
	}}
	client := &Client{client: fake, pathPrefix: "/inference/"}

	_, matched, err := client.Authorize(context.Background(), "/v1/chat/completions", "Bearer ignored", "ignored")
	require.NoError(t, err)
	require.False(t, matched)
	require.Nil(t, fake.request)

	decision, matched, err := client.Authorize(
		context.Background(), "/inference/v1/chat/completions", "Bearer tenant-key", "deployment-id",
	)
	require.NoError(t, err)
	require.True(t, matched)
	require.False(t, decision.Allowed)
	require.Equal(t, int32(503), decision.Status)
	require.Equal(t, "30", decision.Headers[1].Value)
	require.JSONEq(t, `{"error":{"code":"DEPLOYMENT_SCALING_UP"}}`, string(decision.Body))
	httpRequest := fake.request.Attributes.Request.Http
	require.Equal(t, "Bearer tenant-key", httpRequest.Headers["authorization"])
	require.Equal(t, "deployment-id", httpRequest.Headers["x-ai-eg-model"])
}

func TestAuthorizeReturnsCanonicalAllowedModel(t *testing.T) {
	fake := &fakeAuthClient{response: &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.OK)},
		HttpResponse: &authv3.CheckResponse_OkResponse{OkResponse: &authv3.OkHttpResponse{Headers: []*corev3.HeaderValueOption{
			{Header: &corev3.HeaderValue{Key: "x-ai-eg-model", Value: "canonical-id"}},
		}}},
	}}
	decision, matched, err := (&Client{client: fake, pathPrefix: "/inference/"}).Authorize(
		context.Background(), "/inference/v1/chat/completions", "Bearer tenant-key", "mr-canonical-id",
	)
	require.NoError(t, err)
	require.True(t, matched)
	require.True(t, decision.Allowed)
	require.Equal(t, "canonical-id", decision.Model)
}
