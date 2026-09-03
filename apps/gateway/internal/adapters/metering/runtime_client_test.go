//go:build !nitro_enclave

package metering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestRuntimeClientAuthenticateHashesAPIKeyLocally(t *testing.T) {
	const rawKey = "nxs_secret-value"
	wantHash := sha256.Sum256([]byte(rawKey))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/gateway/authenticate" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var request authenticateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.KeyHash != hex.EncodeToString(wantHash[:]) {
			t.Fatalf("key_hash = %q", request.KeyHash)
		}
		if request.KeyHash == rawKey {
			t.Fatal("raw API key crossed the runtime boundary")
		}
		_, _ = w.Write([]byte(`{
			"account":{"id":"acc-1","status":"active"},
			"api_key":{"id":"key-1","account_id":"acc-1","key_prefix":"nxs_1234","active":true,"pii_mode":"balanced"}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", time.Second)
	auth, err := client.AuthenticateAPIKey(context.Background(), rawKey)
	if err != nil {
		t.Fatalf("AuthenticateAPIKey: %v", err)
	}
	if auth.Account.ID != "acc-1" || auth.APIKey.ID != "key-1" || auth.APIKey.PIIMode != "balanced" {
		t.Fatalf("auth = %#v", auth)
	}
}

func TestRuntimeClientPreservesUserAuthenticationErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		errorType string
		code      string
		message   string
	}{
		{name: "invalid key", status: http.StatusUnauthorized, errorType: domain.ErrTypeAuthentication, code: domain.ErrCodeInvalidAPIKey, message: "invalid API key"},
		{name: "inactive key", status: http.StatusUnauthorized, errorType: domain.ErrTypeAuthentication, code: domain.ErrCodeInactiveAPIKey, message: "API key is inactive"},
		{name: "inactive account", status: http.StatusForbidden, errorType: domain.ErrTypePermission, code: domain.ErrCodeInactiveAccount, message: "Account is inactive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(errorResponse{Error: struct {
					Type    string `json:"type"`
					Code    string `json:"code"`
					Message string `json:"message"`
				}{Type: tt.errorType, Code: tt.code, Message: tt.message}})
			}))
			defer server.Close()

			_, err := NewClient(server.URL, "token", time.Second).AuthenticateAPIKey(context.Background(), "nxs_bad")
			gatewayErr, ok := err.(*domain.GatewayError)
			if !ok || gatewayErr.HTTPStatus != tt.status || gatewayErr.Code != tt.code || gatewayErr.Message != tt.message {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestRuntimeClientMapsInternalAuthenticationFailureToInternalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","code":"internal_authentication_failed","message":"invalid internal metering token"}}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "wrong-token", time.Second).AuthenticateAPIKey(context.Background(), "nxs_valid")
	assertInternalMeteringError(t, err)
}

func TestRuntimeClientMapsUnexpectedClientErrorsToInternalError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "invalid internal request", status: http.StatusBadRequest, body: `{"error":{"type":"invalid_request_error","code":"invalid_field","message":"bad internal request"}}`},
		{name: "unknown internal route", status: http.StatusNotFound, body: `404 page not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := NewClient(server.URL, "token", time.Second).GetPublicModel(context.Background(), "model")
			assertInternalMeteringError(t, err)
		})
	}
}

func TestRuntimeClientPreservesUserNotFoundErrors(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "router",
			call: func(client *Client) error {
				_, err := client.GetRouter(context.Background(), "missing-router")
				return err
			},
		},
		{
			name: "proof",
			call: func(client *Client) error {
				_, err := client.GetTinfoilTransportProof(context.Background(), "acc-1", "missing-response")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"not_found","message":"resource not found"}}`))
			}))
			defer server.Close()

			err := tt.call(NewClient(server.URL, "token", time.Second))
			gatewayErr, ok := err.(*domain.GatewayError)
			if !ok || gatewayErr.HTTPStatus != http.StatusNotFound || gatewayErr.Code != domain.ErrCodeNotFound {
				t.Fatalf("error = %#v, want user-facing not found", err)
			}
		})
	}
}

func TestRuntimeClientMapsMissingSuccessBodyToInternalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "token", time.Second).AuthenticateAPIKey(context.Background(), "nxs_valid")
	assertInternalMeteringError(t, err)
}

func TestDecodeJSONResponseDoesNotMutateOutputOnFailure(t *testing.T) {
	type response struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	out := response{Value: "original", Count: 7}
	err := decodeJSONResponse(strings.NewReader(`{"value":"stale","count":"not-an-integer"}`), &out)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if out.Value != "original" || out.Count != 7 {
		t.Fatalf("output mutated after failed decode: %#v", out)
	}
}

func TestDecodeJSONResponseRejectsMultipleValues(t *testing.T) {
	var out map[string]any
	if err := decodeJSONResponse(strings.NewReader(`{} {}`), &out); err == nil {
		t.Fatal("expected multiple JSON values to be rejected")
	}
}

func TestRuntimeClientRejectsInvalidAuthenticationSuccess(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*authenticateResponse)
	}{
		{name: "empty object", mutate: func(response *authenticateResponse) { *response = authenticateResponse{} }},
		{name: "mismatched account", mutate: func(response *authenticateResponse) { response.APIKey.AccountID = "acc-2" }},
		{name: "inactive account", mutate: func(response *authenticateResponse) { response.Account.Status = domain.AccountStatusInactive }},
		{name: "inactive key", mutate: func(response *authenticateResponse) { response.APIKey.Active = false }},
		{name: "invalid pii mode", mutate: func(response *authenticateResponse) { response.APIKey.PIIMode = "everything" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := validAuthenticateResponse()
			tt.mutate(&response)
			server := jsonResponseServer(t, response)
			defer server.Close()

			_, err := NewClient(server.URL, "token", time.Second).AuthenticateAPIKey(context.Background(), "nxs_valid")
			assertInternalMeteringError(t, err)
		})
	}
}

func TestRuntimeClientRejectsInvalidModelSuccess(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*runtimePublicModel)
	}{
		{name: "empty object", mutate: func(model *runtimePublicModel) { *model = runtimePublicModel{} }},
		{name: "mismatched identity", mutate: func(model *runtimePublicModel) { model.PublicModelID = "other/model" }},
		{name: "missing provider field", mutate: func(model *runtimePublicModel) { model.ProviderConfig.APIKeySecretRef = "" }},
		{name: "inactive model", mutate: func(model *runtimePublicModel) { model.Active = false }},
		{name: "inactive provider", mutate: func(model *runtimePublicModel) { model.ProviderConfig.Active = false }},
		{name: "missing capability", mutate: func(model *runtimePublicModel) { model.SupportsTools = nil }},
		{name: "missing limit", mutate: func(model *runtimePublicModel) { model.MaxOutputTokens = 0 }},
		{name: "missing price", mutate: func(model *runtimePublicModel) { model.InputPricePer1MTokensMicrocents = nil }},
		{name: "negative price", mutate: func(model *runtimePublicModel) { model.InputPricePer1MTokensMicrocents = int64Pointer(-1) }},
		{name: "invalid fallback", mutate: func(model *runtimePublicModel) { model.Fallback = &runtimeProviderTarget{} }},
		{name: "duplicate fallback", mutate: func(model *runtimePublicModel) {
			model.Fallback = &runtimeProviderTarget{
				ProviderModelID: model.ProviderModelID, UpstreamModelName: model.UpstreamModelName,
				ProviderConfig: model.ProviderConfig,
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := validRuntimePublicModel("openai/gpt-test")
			tt.mutate(&model)
			server := jsonResponseServer(t, model)
			defer server.Close()

			_, err := NewClient(server.URL, "token", time.Second).GetPublicModel(context.Background(), "openai/gpt-test")
			assertInternalMeteringError(t, err)
		})
	}
}

func TestRuntimeClientMapsProviderFallback(t *testing.T) {
	response := validRuntimePublicModel("openai/gpt-test")
	response.Fallback = &runtimeProviderTarget{
		ProviderModelID: "fallback-model", UpstreamModelName: "fallback-upstream",
		ProviderConfig: runtimeProviderConfig{
			ID: "fallback-config", ProviderName: "fallback", BaseURL: "https://fallback.test",
			APIKeySecretRef: "FALLBACK_API_KEY", Active: true,
		},
	}
	server := jsonResponseServer(t, response)
	defer server.Close()

	model, err := NewClient(server.URL, "token", time.Second).GetPublicModel(context.Background(), "openai/gpt-test")
	if err != nil {
		t.Fatalf("GetPublicModel: %v", err)
	}
	if model.Fallback == nil || model.Fallback.ProviderModelID != "fallback-model" ||
		model.Fallback.ProviderConfig.ProviderName != "fallback" {
		t.Fatalf("fallback = %#v", model.Fallback)
	}
}

func TestRuntimeClientModelListRequiresArrayAndValidUniqueItems(t *testing.T) {
	t.Run("empty array is valid", func(t *testing.T) {
		server := rawResponseServer(`[]`)
		defer server.Close()
		models, err := NewClient(server.URL, "token", time.Second).ListPublicModels(context.Background())
		if err != nil || models == nil || len(models) != 0 {
			t.Fatalf("models = %#v, error = %v; want non-nil empty list", models, err)
		}
	})

	tests := []struct {
		name string
		body any
	}{
		{name: "null", body: nil},
		{name: "invalid item", body: []runtimePublicModel{{}}},
		{name: "duplicate identity", body: []runtimePublicModel{
			validRuntimePublicModel("openai/gpt-test"),
			validRuntimePublicModel("openai/gpt-test"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := jsonResponseServer(t, tt.body)
			defer server.Close()
			_, err := NewClient(server.URL, "token", time.Second).ListPublicModels(context.Background())
			assertInternalMeteringError(t, err)
		})
	}
}

func TestRuntimeClientRejectsInvalidRouterSuccess(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*runtimeRouter)
	}{
		{name: "empty object", mutate: func(router *runtimeRouter) { *router = runtimeRouter{} }},
		{name: "mismatched identity", mutate: func(router *runtimeRouter) { router.RouterID = "other" }},
		{name: "invalid strategy config", mutate: func(router *runtimeRouter) { router.StrategyConfig = json.RawMessage(`null`) }},
		{name: "inactive router", mutate: func(router *runtimeRouter) { router.Active = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := validRuntimeRouter("auto")
			tt.mutate(&router)
			server := jsonResponseServer(t, router)
			defer server.Close()

			_, err := NewClient(server.URL, "token", time.Second).GetRouter(context.Background(), "auto")
			assertInternalMeteringError(t, err)
		})
	}
}

func TestRuntimeClientRouterListRejectsNullButAcceptsEmptyArray(t *testing.T) {
	t.Run("empty array is valid", func(t *testing.T) {
		server := rawResponseServer(`[]`)
		defer server.Close()
		routers, err := NewClient(server.URL, "token", time.Second).ListRouters(context.Background())
		if err != nil || routers == nil || len(routers) != 0 {
			t.Fatalf("routers = %#v, error = %v; want non-nil empty list", routers, err)
		}
	})

	t.Run("null is invalid", func(t *testing.T) {
		server := rawResponseServer(`null`)
		defer server.Close()
		_, err := NewClient(server.URL, "token", time.Second).ListRouters(context.Background())
		assertInternalMeteringError(t, err)
	})
}

func TestRuntimeClientRejectsInvalidProofSuccess(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*runtimeProof)
	}{
		{name: "empty object", mutate: func(proof *runtimeProof) { *proof = runtimeProof{} }},
		{name: "mismatched account", mutate: func(proof *runtimeProof) { proof.AccountID = "acc-2" }},
		{name: "mismatched response", mutate: func(proof *runtimeProof) { proof.ProviderResponseID = "resp-2" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proof := validRuntimeProof("acc-1", "resp-1")
			tt.mutate(&proof)
			server := jsonResponseServer(t, proof)
			defer server.Close()

			_, err := NewClient(server.URL, "token", time.Second).GetTinfoilTransportProof(context.Background(), "acc-1", "resp-1")
			assertInternalMeteringError(t, err)
		})
	}
}

func TestRuntimeClientRetryDoesNotReusePartiallyDecodedModel(t *testing.T) {
	var attempts atomic.Int32
	validBody, err := json.Marshal(validRuntimePublicModel("openai/gpt-test"))
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	partialBody := strings.TrimSuffix(string(validBody), "}") + `,"max_context_window":"bad"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			_, _ = w.Write([]byte(partialBody))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	_, err = NewClient(server.URL, "token", time.Second).GetPublicModel(context.Background(), "openai/gpt-test")
	assertInternalMeteringError(t, err)
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestRuntimeClientMapsModelPricesWithoutPrecisionLoss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/internal/gateway/models/openai%2Fgpt-test" {
			t.Fatalf("escaped path = %q", r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{
			"id":"model-uuid","public_model_id":"openai/gpt-test","display_name":"GPT Test",
			"provider_model_id":"provider-model","upstream_model_name":"gpt-test",
			"provider_config":{"id":"provider-uuid","provider_name":"openai","base_url":"https://example.test","api_key_secret_ref":"OPENAI_API_KEY","active":true},
			"supports_chat_completions":true,"supports_chat_completions_stream":true,
			"supports_tools":false,"supports_parallel_tool_calls":false,
			"supports_structured_output":false,"supports_reasoning":false,
			"proof_mode":"none","max_context_window":128000,"max_output_tokens":4096,
			"input_price_per_1m_tokens_microcents":75000000,
			"output_price_per_1m_tokens_microcents":450000000,
			"cache_read_price_per_1m_tokens_microcents":12500000,
			"currency":"EUR","active":true
		}`))
	}))
	defer server.Close()

	model, err := NewClient(server.URL, "", time.Second).GetPublicModel(context.Background(), "openai/gpt-test")
	if err != nil {
		t.Fatalf("GetPublicModel: %v", err)
	}
	if model.InputPricePerMillion.String() != "0.75" || model.OutputPricePerMillion.String() != "4.5" {
		t.Fatalf("prices = (%s, %s)", model.InputPricePerMillion, model.OutputPricePerMillion)
	}
	if model.CacheReadPricePerMillion == nil || model.CacheReadPricePerMillion.String() != "0.125" {
		t.Fatalf("cache read price = %v", model.CacheReadPricePerMillion)
	}
}

func TestRuntimeClientPreservesRouterStrategy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"router-uuid","router_id":"auto","display_name":"Auto",
			 "fallback_public_model_id":"openai/gpt-test","strategy_type":"embedding",
			 "strategy_config":{"threshold":0.4},"active":true,
			 "created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	routers, err := NewClient(server.URL, "", time.Second).ListRouters(context.Background())
	if err != nil {
		t.Fatalf("ListRouters: %v", err)
	}
	if len(routers) != 1 || routers[0].StrategyType != domain.RouterStrategyEmbedding || string(routers[0].StrategyConfig) != `{"threshold":0.4}` {
		t.Fatalf("routers = %#v", routers)
	}
}

func TestRuntimeClientTinfoilProofUsesSnakeCaseAndEscapedQuery(t *testing.T) {
	var postSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			postSeen = true
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode proof: %v", err)
			}
			if _, ok := body["provider_response_id"]; !ok {
				t.Fatalf("proof payload = %#v", body)
			}
			if _, ok := body["ProviderResponseID"]; ok {
				t.Fatalf("proof leaked Go field names: %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			if r.URL.Query().Get("account_id") != "acc 1" || r.URL.Query().Get("provider_response_id") != "resp/1" {
				t.Fatalf("query = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{
				"id":"proof-1","account_id":"acc 1","api_key_id":"key-1","provider":"tinfoil",
				"public_model_id":"tinfoil/model","upstream_model_id":"model",
				"provider_response_id":"resp/1","status":"verified","created_at":"2026-01-01T00:00:00Z"
			}`))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", time.Second)
	proof := domain.TinfoilTransportProof{
		AccountID: "acc 1", APIKeyID: "key-1", Provider: "tinfoil",
		PublicModelID: "tinfoil/model", UpstreamModelID: "model",
		ProviderResponseID: "resp/1", Status: domain.ProofStatusVerified,
	}
	if err := client.UpsertTinfoilTransportProof(context.Background(), proof); err != nil {
		t.Fatalf("UpsertTinfoilTransportProof: %v", err)
	}
	got, err := client.GetTinfoilTransportProof(context.Background(), "acc 1", "resp/1")
	if err != nil {
		t.Fatalf("GetTinfoilTransportProof: %v", err)
	}
	if !postSeen || got.ProviderResponseID != "resp/1" {
		t.Fatalf("proof = %#v, postSeen = %v", got, postSeen)
	}
}

func TestRuntimeClientPreservesMeteringErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"unsupported_model","message":"missing"}}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "", time.Second).GetPublicModel(context.Background(), "missing")
	var gatewayErr *domain.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != domain.ErrCodeUnsupportedModel || gatewayErr.HTTPStatus != http.StatusNotFound {
		t.Fatalf("error = %#v", err)
	}
}

func validAuthenticateResponse() authenticateResponse {
	return authenticateResponse{
		Account: runtimeAccount{ID: "acc-1", Status: domain.AccountStatusActive},
		APIKey: runtimeAPIKey{
			ID: "key-1", AccountID: "acc-1", KeyPrefix: "nxs_1234",
			Active: true, PIIMode: domain.APIKeyPIIModeBalanced,
		},
	}
}

func validRuntimePublicModel(publicModelID string) runtimePublicModel {
	return runtimePublicModel{
		ID: "model-uuid", PublicModelID: publicModelID, DisplayName: "GPT Test",
		ProviderModelID: "provider-model", UpstreamModelName: "gpt-test",
		ProviderConfig: runtimeProviderConfig{
			ID: "provider-uuid", ProviderName: "openai", BaseURL: "https://example.test",
			APIKeySecretRef: "OPENAI_API_KEY", Active: true,
		},
		SupportsChatCompletions: boolPointer(true), SupportsChatCompletionsStream: boolPointer(true),
		SupportsTools: boolPointer(false), SupportsParallelToolCalls: boolPointer(false),
		SupportsStructuredOutput: boolPointer(false), SupportsReasoning: boolPointer(false),
		ProofMode: domain.ProofModeNone, MaxContextWindow: 128000, MaxOutputTokens: 4096,
		InputPricePer1MTokensMicrocents: int64Pointer(0), OutputPricePer1MTokensMicrocents: int64Pointer(0),
		Currency: "EUR", Active: true,
	}
}

func validRuntimeRouter(routerID string) runtimeRouter {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return runtimeRouter{
		ID: "router-uuid", RouterID: routerID, DisplayName: "Auto",
		FallbackPublicModelID: "openai/gpt-test", StrategyType: "embedding",
		StrategyConfig: json.RawMessage(`{"threshold":0.4}`), Active: true,
		CreatedAt: now, UpdatedAt: now,
	}
}

func validRuntimeProof(accountID, providerResponseID string) runtimeProof {
	return runtimeProof{
		ID: "proof-uuid", AccountID: accountID, APIKeyID: "key-1", Provider: "tinfoil",
		PublicModelID: "tinfoil/model", UpstreamModelID: "model",
		ProviderResponseID: providerResponseID, Status: domain.ProofStatusVerified,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func jsonResponseServer(t *testing.T, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func rawResponseServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

func boolPointer(value bool) *bool {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}
