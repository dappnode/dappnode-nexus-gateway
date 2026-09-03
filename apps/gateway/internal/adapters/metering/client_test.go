//go:build !nitro_enclave

package metering

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestClientReserve_MapsInsufficientBalance(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"type":"permission_error","code":"insufficient_balance","message":"no credit"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", time.Second)
	_, err := client.Reserve(context.Background(), testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{PublicModelID: "model"}, testModel(), "gw-1")
	var gatewayErr *domain.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != domain.ErrCodeInsufficientBalance {
		t.Fatalf("err = %#v, want insufficient balance", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want deterministic 4xx response not to be retried", got)
	}
}

func TestClientReserve_MapsInternalContractErrorsToInternalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"conflict","message":"reservation operation is no longer pending"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", time.Second)
	_, err := client.Reserve(context.Background(), testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{PublicModelID: "model"}, testModel(), "gw-1")
	assertInternalMeteringError(t, err)
}

func TestClientReserve_ExhaustedServerErrorsReturnInternalError(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"internal_error","code":"internal_error","message":"database failed"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", time.Second)
	_, err := client.Reserve(context.Background(), testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{PublicModelID: "model"}, testModel(), "gw-1")
	assertInternalMeteringError(t, err)
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestClientReserve_TransportFailureReturnsInternalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()

	client := NewClient(serverURL, "token", time.Second)
	_, err := client.Reserve(context.Background(), testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{PublicModelID: "model"}, testModel(), "gw-1")
	assertInternalMeteringError(t, err)
}

func TestClientReserve_RetriesAmbiguousResponseWithStableRequestID(t *testing.T) {
	var attempts atomic.Int32
	const reservationRequestID = "reservation-request-1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body reserveRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode reservation request: %v", err)
		}
		if body.GatewayRequestID != reservationRequestID {
			t.Fatalf("gateway_request_id = %q, want %q", body.GatewayRequestID, reservationRequestID)
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"reservation_id":"reservation-1"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", time.Second)
	reservationID, err := client.Reserve(context.Background(), testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{PublicModelID: "model"}, testModel(), reservationRequestID)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if reservationID != "reservation-1" {
		t.Fatalf("reservation ID = %q, want reservation-1", reservationID)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestClientRecordSuccess_RetriesServerErrors(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/metering/reservations/res-1/complete" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"usage_event_id":"usage-1"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", time.Second)
	err := client.RecordSuccess(context.Background(), "res-1", testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{}, domain.GenerateResult{
		ID:              "provider-req-1",
		ProviderName:    "openai",
		ProviderModelID: "gpt-test",
		Usage:           &domain.Usage{PromptTokens: 1, CompletionTokens: 1},
	}, testModel(), 123)
	if err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestClientRecordSuccess_UsesActualExecutionTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body completeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.ProviderName != "fallback" || body.ProviderModelID != "fallback-model" {
			t.Fatalf("provider target = %s/%s, want fallback/fallback-model", body.ProviderName, body.ProviderModelID)
		}
		_, _ = w.Write([]byte(`{"usage_event_id":"usage-1"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", time.Second)
	err := client.RecordSuccess(context.Background(), "res-1", testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{}, domain.GenerateResult{
		ProviderName: "fallback", ProviderModelID: "fallback-model",
	}, domain.PublicModel{
		ProviderModelID: "primary-model",
		ProviderConfig:  domain.ProviderConfig{ProviderName: "primary"},
	}, 1)
	if err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
}

func TestClientRecordSuccess_UsesStableSnakeCaseUsageContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "cache_creation_tokens", "cache_read_tokens"} {
			if _, ok := body.Usage[key]; !ok {
				t.Errorf("usage payload missing %q: %#v", key, body.Usage)
			}
		}
		if _, ok := body.Usage["PromptTokens"]; ok {
			t.Errorf("usage payload leaked Go field names: %#v", body.Usage)
		}
		_, _ = w.Write([]byte(`{"usage_event_id":"usage-1"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", time.Second)
	err := client.RecordSuccess(context.Background(), "res-1", testAuth(), domain.EndpointChatCompletions, domain.GenerateRequest{}, domain.GenerateResult{
		Usage: &domain.Usage{
			PromptTokens:        1,
			CompletionTokens:    2,
			TotalTokens:         3,
			CacheCreationTokens: 4,
			CacheReadTokens:     5,
		},
	}, testModel(), 123)
	if err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
}

func TestClientRecordFailure_WithReservationRetriesServerErrors(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/metering/usage-failures" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", time.Second)
	reservationID := "res-1"
	err := client.RecordFailure(context.Background(), &reservationID, nil, domain.EndpointChatCompletions, nil, nil, domain.ErrProviderUnavailable("openai"), nil, 123)
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func testAuth() domain.AuthContext {
	return domain.AuthContext{
		Account: domain.Account{ID: "acc-1"},
		APIKey:  domain.APIKey{ID: "key-1"},
	}
}

func testModel() domain.PublicModel {
	return domain.PublicModel{
		PublicModelID:   "model",
		ProviderModelID: "gpt-test",
		ProviderConfig:  domain.ProviderConfig{ProviderName: "openai"},
	}
}

func assertInternalMeteringError(t *testing.T, err error) {
	t.Helper()
	gatewayErr, ok := err.(*domain.GatewayError)
	if !ok || gatewayErr.HTTPStatus != http.StatusInternalServerError || gatewayErr.Type != domain.ErrTypeInternal || gatewayErr.Code != domain.ErrCodeInternalError {
		t.Fatalf("err = %#v, want generic internal error", err)
	}
	if gatewayErr.Message != "an internal error occurred" {
		t.Fatalf("message = %q, want generic internal error message", gatewayErr.Message)
	}
	if gatewayErr.Metadata["dependency"] != "metering" {
		t.Fatalf("metadata = %#v, want metering dependency", gatewayErr.Metadata)
	}
}
