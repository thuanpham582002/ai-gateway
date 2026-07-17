// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache License 2.0 is available at the root of this repository.

package compatibleauth

import (
	"context"
	"errors"
	"strings"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
)

const requestTimeout = 5 * time.Second

type Header struct {
	Name  string
	Value string
}

type Decision struct {
	Allowed bool
	Status  int32
	Body    []byte
	Headers []Header
	Model   string
}

type Authorizer interface {
	Authorize(ctx context.Context, path, authorization, model string) (Decision, bool, error)
}

type authClient interface {
	Check(context.Context, *authv3.CheckRequest, ...grpc.CallOption) (*authv3.CheckResponse, error)
}

type Client struct {
	client     authClient
	connection *grpc.ClientConn
	pathPrefix string
}

type unavailableAuthorizer struct {
	pathPrefix string
}

func NewUnavailableAuthorizer(pathPrefix string) Authorizer {
	return &unavailableAuthorizer{pathPrefix: strings.TrimSpace(pathPrefix)}
}

func (a *unavailableAuthorizer) Authorize(
	_ context.Context,
	path string,
	_ string,
	_ string,
) (Decision, bool, error) {
	if a == nil || a.pathPrefix == "" || !strings.HasPrefix(path, a.pathPrefix) {
		return Decision{}, false, nil
	}
	return Decision{}, true, errors.New("compatible auth is not configured")
}

func Dial(address, pathPrefix string) (*Client, error) {
	address = strings.TrimSpace(address)
	pathPrefix = strings.TrimSpace(pathPrefix)
	if address == "" || pathPrefix == "" {
		return nil, errors.New("compatible auth address and path prefix are required")
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Client{
		client: authv3.NewAuthorizationClient(connection), connection: connection, pathPrefix: pathPrefix,
	}, nil
}

func (c *Client) Close() error {
	if c == nil || c.connection == nil {
		return nil
	}
	return c.connection.Close()
}

func (c *Client) Authorize(
	ctx context.Context,
	path string,
	authorization string,
	model string,
) (Decision, bool, error) {
	if c == nil || !strings.HasPrefix(path, c.pathPrefix) {
		return Decision{}, false, nil
	}
	request := &authv3.CheckRequest{Attributes: &authv3.AttributeContext{Request: &authv3.AttributeContext_Request{
		Http: &authv3.AttributeContext_HttpRequest{Headers: map[string]string{
			"authorization": authorization,
			"x-ai-eg-model": model,
		}},
	}}}
	checkCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	response, err := c.client.Check(checkCtx, request)
	if err != nil {
		return Decision{}, true, err
	}
	if response.GetOkResponse() != nil && response.GetStatus().GetCode() == int32(codes.OK) {
		decision := Decision{Allowed: true}
		for _, option := range response.GetOkResponse().GetHeaders() {
			header := header(option)
			if strings.EqualFold(header.Name, "x-ai-eg-model") {
				decision.Model = header.Value
			}
		}
		return decision, true, nil
	}
	denied := response.GetDeniedResponse()
	if denied == nil || denied.GetStatus() == nil {
		return Decision{}, true, errors.New("compatible auth returned no HTTP decision")
	}
	decision := Decision{Status: int32(denied.GetStatus().GetCode()), Body: []byte(denied.GetBody())}
	for _, option := range denied.GetHeaders() {
		decision.Headers = append(decision.Headers, header(option))
	}
	return decision, true, nil
}

func header(option *corev3.HeaderValueOption) Header {
	if option == nil || option.Header == nil {
		return Header{}
	}
	value := option.Header.Value
	if len(option.Header.RawValue) > 0 {
		value = string(option.Header.RawValue)
	}
	return Header{Name: option.Header.Key, Value: value}
}
