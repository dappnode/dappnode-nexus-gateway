//go:build !nitro_enclave

package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestClientRouteMapsInternalRouterFailuresToInternalError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "unknown route", status: http.StatusNotFound, body: "404 page not found"},
		{name: "service failure", status: http.StatusServiceUnavailable, body: `{"error":{"type":"internal_error","code":"internal_error","message":"router failed"}}`},
		{name: "mismatched user code", status: http.StatusInternalServerError, body: `{"error":{"type":"invalid_request_error","code":"not_found","message":"router failed"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := NewClient(server.URL, time.Second).Route(context.Background(), domain.RouteRequest{RouterID: "auto"})
			gatewayErr, ok := err.(*domain.GatewayError)
			if !ok || gatewayErr.HTTPStatus != http.StatusInternalServerError || gatewayErr.Type != domain.ErrTypeInternal || gatewayErr.Code != domain.ErrCodeInternalError {
				t.Fatalf("error = %#v, want generic internal error", err)
			}
			if gatewayErr.Message != "an internal error occurred" || gatewayErr.Metadata["dependency"] != "router" {
				t.Fatalf("error = %#v, want generic response with router log metadata", gatewayErr)
			}
		})
	}
}

func TestClientRoutePreservesUserRouterErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"not_found","message":"router not found"}}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, time.Second).Route(context.Background(), domain.RouteRequest{RouterID: "missing"})
	gatewayErr, ok := err.(*domain.GatewayError)
	if !ok || gatewayErr.HTTPStatus != http.StatusNotFound || gatewayErr.Code != domain.ErrCodeUnsupportedModel {
		t.Fatalf("error = %#v, want unsupported model", err)
	}
}
