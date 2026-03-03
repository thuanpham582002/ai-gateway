// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// PostRouteModify allows an extension to modify routes after they are generated.
func (s *Server) PostRouteModify(_ context.Context, req *egextension.PostRouteModifyRequest) (*egextension.PostRouteModifyResponse, error) {
	if req.Route == nil {
		return nil, nil
	}

	// Check if we have backend extension resources (InferencePool resources).
	if req.PostRouteContext == nil || len(req.PostRouteContext.ExtensionResources) == 0 {
		// No backend extension resources, skip.
		return &egextension.PostRouteModifyResponse{Route: req.Route}, nil
	}

	// Parse InferencePool resources from BackendExtensionResources.
	inferencePools := s.constructInferencePoolsFrom(req.PostRouteContext.ExtensionResources)

	// If we found InferencePool(s), configure the route with the ext_proc per-route config.
	// InferencePool configuration only applies to forwarding routes (RouteAction).
	// Non-forwarding routes (e.g. DirectResponse, Redirect) cannot route to an InferencePool.
	if len(inferencePools) > 0 {
		if routeAction := req.Route.GetRoute(); routeAction != nil {
			routeAction.HostRewriteSpecifier = &routev3.RouteAction_AutoHostRewrite{
				AutoHostRewrite: wrapperspb.Bool(false),
			}
		}
		if req.Route.TypedPerFilterConfig == nil {
			req.Route.TypedPerFilterConfig = make(map[string]*anypb.Any)
		}
		for _, pool := range inferencePools {
			buildEPPMetadataForRoute(req.Route, pool)
		}
	}

	return &egextension.PostRouteModifyResponse{Route: req.Route}, nil
}
