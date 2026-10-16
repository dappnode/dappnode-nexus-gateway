package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type routerTestLogger struct{}

func (*routerTestLogger) Debug(string, ...any) {}
func (*routerTestLogger) Info(string, ...any)  {}
func (*routerTestLogger) Warn(string, ...any)  {}
func (*routerTestLogger) Error(string, ...any) {}

func TestNewRouter_PreservesLegacyChatRouteWithoutConfidentialHandler(t *testing.T) {
	logger := &routerTestLogger{}
	router := NewRouter(
		handlers.NewHealthHandler(),
		handlers.NewModelsHandler(nil),
		handlers.NewChatCompletionsHandler(nil, logger),
		handlers.NewSystemOneHandler(nil, logger),
		nil,
		nil,
		nil,
		logger,
	)

	legacyRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test"}`))
	legacyResponse := httptest.NewRecorder()
	router.ServeHTTP(legacyResponse, legacyRequest)
	if legacyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("legacy status = %d, want existing 401 auth behavior", legacyResponse.Code)
	}

	evaluationResponse := httptest.NewRecorder()
	router.ServeHTTP(evaluationResponse, httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{}`)))
	if evaluationResponse.Code != http.StatusUnauthorized {
		t.Fatalf("systemone status = %d", evaluationResponse.Code)
	}

	confidentialRequest := httptest.NewRequest(http.MethodPost, "/v1/confidential/chat/completions", strings.NewReader(`{"model":"test"}`))
	confidentialResponse := httptest.NewRecorder()
	router.ServeHTTP(confidentialResponse, confidentialRequest)
	if confidentialResponse.Code != http.StatusNotFound {
		t.Fatalf("disabled confidential route status = %d, want 404", confidentialResponse.Code)
	}

	keyDiscoveryRequest := httptest.NewRequest(http.MethodGet, "/.well-known/hpke-keys", nil)
	keyDiscoveryResponse := httptest.NewRecorder()
	router.ServeHTTP(keyDiscoveryResponse, keyDiscoveryRequest)
	if keyDiscoveryResponse.Code != http.StatusNotFound {
		t.Fatalf("unauthenticated EHBP key discovery status = %d, want 404", keyDiscoveryResponse.Code)
	}
}

// Requests are counted under the route they matched, though the middleware
// in between hands the mux a copy of the request.
func TestNewRouter_LabelsMetricsWithTheMatchedRoute(t *testing.T) {
	logger := &routerTestLogger{}
	router := NewRouter(handlers.NewHealthHandler(), handlers.NewModelsHandler(nil), handlers.NewChatCompletionsHandler(nil, logger), nil, nil, nil, nil, logger)
	health := metrics.HTTPRequestsTotal.WithLabelValues("GET /healthz", http.MethodGet, "2xx")
	unmatched := metrics.HTTPRequestsTotal.WithLabelValues("unmatched", http.MethodGet, "4xx")
	healthBefore, unmatchedBefore := testutil.ToFloat64(health), testutil.ToFloat64(unmatched)

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	if got := testutil.ToFloat64(health) - healthBefore; got != 1 {
		t.Fatalf("GET /healthz count delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(unmatched) - unmatchedBefore; got != 1 {
		t.Fatalf("unmatched count delta = %v, want 1", got)
	}
}
