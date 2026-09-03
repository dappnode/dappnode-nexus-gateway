package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/shopspring/decimal"
)

// --- Mock implementations ---

type mockAuthService struct {
	authCtx domain.AuthContext
	err     error
}

func (m *mockAuthService) AuthenticateAPIKey(_ context.Context, _ string) (domain.AuthContext, error) {
	return m.authCtx, m.err
}

type mockModelCatalog struct {
	models []domain.PublicModel
	model  domain.PublicModel
	err    error
}

func (m *mockModelCatalog) ListPublicModels(_ context.Context) ([]domain.PublicModel, error) {
	return m.models, m.err
}

func (m *mockModelCatalog) GetPublicModel(_ context.Context, _ string) (domain.PublicModel, error) {
	return m.model, m.err
}

func (m *mockModelCatalog) ListRouters(_ context.Context) ([]domain.RouterEntry, error) {
	return nil, nil
}

func (m *mockModelCatalog) GetRouter(_ context.Context, routerID string) (domain.RouterEntry, error) {
	return domain.RouterEntry{}, domain.ErrNotFound("router", routerID)
}

type mockUsageMeter struct{}

func (m *mockUsageMeter) Reserve(_ context.Context, _ domain.AuthContext, _ string, _ domain.GenerateRequest, _ domain.PublicModel, _ string) (string, error) {
	return "mock-reservation", nil
}

func (m *mockUsageMeter) RecordSuccess(_ context.Context, _ string, _ domain.AuthContext, _ string, _ domain.GenerateRequest, _ domain.GenerateResult, _ domain.PublicModel, _ int64) error {
	return nil
}

func (m *mockUsageMeter) RecordFailure(_ context.Context, _ *string, _ *domain.AuthContext, _ string, _ *domain.GenerateRequest, _ *domain.PublicModel, _ error, _ *domain.Usage, _ int64) error {
	return nil
}

type mockLogger struct{}

func (m *mockLogger) Debug(_ string, _ ...any) {}
func (m *mockLogger) Info(_ string, _ ...any)  {}
func (m *mockLogger) Warn(_ string, _ ...any)  {}
func (m *mockLogger) Error(_ string, _ ...any) {}

// --- Tests ---

func TestHealthHandler(t *testing.T) {
	h := handlers.NewHealthHandler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status = %s, want ok", body["status"])
	}
}

func TestModelsHandler_NoAuthRequired(t *testing.T) {
	catalog := &mockModelCatalog{}
	logger := &mockLogger{}

	svc := services.NewListModelsService(catalog, logger)
	h := handlers.NewModelsHandler(svc)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (endpoint is public)", resp.StatusCode)
	}
}

func TestModelsHandler_ListsModels(t *testing.T) {
	desc := "GPT-4.1 Mini model"
	catalog := &mockModelCatalog{
		models: []domain.PublicModel{
			{
				PublicModelID:                 "openai/gpt-4.1-mini",
				DisplayName:                   "GPT-4.1 Mini",
				Description:                   &desc,
				ProviderConfig:                domain.ProviderConfig{ProviderName: "openai"},
				Active:                        true,
				SupportsChatCompletions:       true,
				SupportsChatCompletionsStream: true,
				SupportsTools:                 true,
				SupportsStructuredOutput:      true,
				MaxContextWindow:              128000,
				MaxOutputTokens:               16000,
				Currency:                      "EUR",
				InputPricePerMillion:          decimal.NewFromFloat(0.4),
				OutputPricePerMillion:         decimal.NewFromFloat(1.6),
			},
			{
				PublicModelID:           "anthropic/claude-sonnet-4",
				DisplayName:             "Claude Sonnet 4",
				ProviderConfig:          domain.ProviderConfig{ProviderName: "anthropic"},
				Active:                  true,
				SupportsChatCompletions: true,
				Currency:                "EUR",
				InputPricePerMillion:    decimal.NewFromFloat(3.0),
				OutputPricePerMillion:   decimal.NewFromFloat(15.0),
			},
		},
	}
	logger := &mockLogger{}

	svc := services.NewListModelsService(catalog, logger)
	h := handlers.NewModelsHandler(svc)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID                          string   `json:"id"`
			Object                      string   `json:"object"`
			OwnedBy                     string   `json:"owned_by"`
			DisplayName                 string   `json:"display_name"`
			Description                 *string  `json:"description,omitempty"`
			ContextSize                 int      `json:"context_size"`
			MaxOutputTokens             int      `json:"max_output_tokens"`
			Currency                    string   `json:"currency"`
			InputPricePer1MTokensCents  int64    `json:"input_price_per_1m_tokens_cents"`
			OutputPricePer1MTokensCents int64    `json:"output_price_per_1m_tokens_cents"`
			InputPricePer1MTokensUSD    float64  `json:"input_price_per_1m_tokens_usd"`
			OutputPricePer1MTokensUSD   float64  `json:"output_price_per_1m_tokens_usd"`
			Features                    []string `json:"features"`
			Endpoints                   []string `json:"endpoints"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&body)

	if body.Object != "list" {
		t.Errorf("object = %s, want list", body.Object)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(body.Data))
	}
	first := body.Data[0]
	if first.ID != "openai/gpt-4.1-mini" {
		t.Errorf("data[0].id = %s, want openai/gpt-4.1-mini", first.ID)
	}
	if first.OwnedBy != "nexus" {
		t.Errorf("data[0].owned_by = %s, want nexus", first.OwnedBy)
	}
	if first.DisplayName != "GPT-4.1 Mini" {
		t.Errorf("data[0].display_name = %s, want GPT-4.1 Mini", first.DisplayName)
	}
	if first.Description == nil || *first.Description != "GPT-4.1 Mini model" {
		t.Errorf("data[0].description mismatch: %+v", first.Description)
	}
	if first.ContextSize != 128000 {
		t.Errorf("data[0].context_size = %d, want 128000", first.ContextSize)
	}
	if first.MaxOutputTokens != 16000 {
		t.Errorf("data[0].max_output_tokens = %d, want 16000", first.MaxOutputTokens)
	}
	if first.Currency != "EUR" {
		t.Errorf("data[0].currency = %s, want EUR", first.Currency)
	}
	if first.InputPricePer1MTokensCents != 40 {
		t.Errorf("data[0].input_price = %d, want 40", first.InputPricePer1MTokensCents)
	}
	if first.OutputPricePer1MTokensCents != 160 {
		t.Errorf("data[0].output_price = %d, want 160", first.OutputPricePer1MTokensCents)
	}
	if first.InputPricePer1MTokensUSD != 0.432 {
		t.Errorf("data[0].input_price_usd = %v, want 0.432", first.InputPricePer1MTokensUSD)
	}
	if first.OutputPricePer1MTokensUSD != 1.728 {
		t.Errorf("data[0].output_price_usd = %v, want 1.728", first.OutputPricePer1MTokensUSD)
	}
	wantFeatures := map[string]bool{
		"streaming":          true,
		"function-calling":   true,
		"structured-outputs": true,
	}
	if len(first.Features) != len(wantFeatures) {
		t.Errorf("data[0].features = %v, want %v", first.Features, wantFeatures)
	}
	for _, f := range first.Features {
		if !wantFeatures[f] {
			t.Errorf("unexpected feature %q in data[0].features", f)
		}
	}
	if len(first.Endpoints) != 1 || first.Endpoints[0] != "chat/completions" {
		t.Errorf("data[0].endpoints = %v, want [chat/completions]", first.Endpoints)
	}
}

func TestModelsHandler_PrivateModelIncludesBaseModel(t *testing.T) {
	catalog := &mockModelCatalog{
		models: []domain.PublicModel{
			{
				PublicModelID:           "private/minimax",
				UpstreamModelName:       "minimax/minimax-m2.7",
				DisplayName:             "Private MiniMax",
				SupportsChatCompletions: true,
				Currency:                "EUR",
			},
		},
	}
	logger := &mockLogger{}

	svc := services.NewListModelsService(catalog, logger)
	h := handlers.NewModelsHandler(svc)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			ID        string  `json:"id"`
			BaseModel *string `json:"base_model,omitempty"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("data len = %d, want 1", len(body.Data))
	}
	if body.Data[0].ID != "private/minimax" {
		t.Fatalf("data[0].id = %s, want private/minimax", body.Data[0].ID)
	}
	if body.Data[0].BaseModel == nil || *body.Data[0].BaseModel != "minimax/minimax-m2.7" {
		t.Fatalf("data[0].base_model = %v, want minimax/minimax-m2.7", body.Data[0].BaseModel)
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
		wantToken  string
		wantErr    bool
	}{
		{"valid", "Bearer sk-test-123", "sk-test-123", false},
		{"missing", "", "", true},
		{"malformed", "Basic abc", "", true},
		{"empty_bearer", "Bearer ", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			token, err := handlers.ExtractBearerToken(req)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token != tt.wantToken {
				t.Errorf("token = %s, want %s", token, tt.wantToken)
			}
		})
	}
}

func TestChatCompletionsHandler_MissingAuth(t *testing.T) {
	auth := &mockAuthService{err: domain.ErrInvalidAPIKey("bad")}
	catalog := &mockModelCatalog{}
	logger := &mockLogger{}
	usage := &mockUsageMeter{}

	genSvc := services.NewGenerateService(auth, catalog, nil, nil, usage, nil, logger)
	chatSvc := services.NewChatCompletionsService(genSvc, logger)
	h := handlers.NewChatCompletionsHandler(chatSvc, logger)

	body := `{"model": "test", "messages": [{"role": "user", "content": "hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Handle(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
