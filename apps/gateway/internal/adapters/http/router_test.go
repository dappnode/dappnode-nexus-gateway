package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
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
