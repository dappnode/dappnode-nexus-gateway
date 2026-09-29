package services

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type stubAuthService struct {
	authCtx domain.AuthContext
	err     error
}

func (s *stubAuthService) AuthenticateAPIKey(_ context.Context, _ string) (domain.AuthContext, error) {
	return s.authCtx, s.err
}

type stubModelCatalog struct {
	models  map[string]domain.PublicModel
	routers map[string]domain.RouterEntry
	model   domain.PublicModel
	err     error
}

func (s *stubModelCatalog) ListPublicModels(_ context.Context) ([]domain.PublicModel, error) {
	return nil, nil
}

func (s *stubModelCatalog) GetPublicModel(_ context.Context, publicModelID string) (domain.PublicModel, error) {
	if s.models != nil {
		model, ok := s.models[publicModelID]
		if !ok {
			return domain.PublicModel{}, domain.ErrUnsupportedModel(publicModelID)
		}
		return model, nil
	}
	return s.model, s.err
}

func (s *stubModelCatalog) ListRouters(_ context.Context) ([]domain.RouterEntry, error) {
	return nil, nil
}

func (s *stubModelCatalog) GetRouter(_ context.Context, routerID string) (domain.RouterEntry, error) {
	router, ok := s.routers[routerID]
	if !ok {
		return domain.RouterEntry{}, domain.ErrNotFound("router", routerID)
	}
	return router, nil
}

type stubRouterClient struct {
	decision domain.RouteDecision
	err      error
	calls    int
}

func (s *stubRouterClient) Route(_ context.Context, _ domain.RouteRequest) (domain.RouteDecision, error) {
	s.calls++
	if s.err != nil {
		return domain.RouteDecision{}, s.err
	}
	return s.decision, nil
}

type stubUsageMeter struct {
	failureCalls          int
	successCalls          int
	reserveCalls          int
	reserveErr            error
	lastReserveReq        domain.GenerateRequest
	lastReservationReqID  string
	lastSuccessReq        domain.GenerateRequest
	lastReservationID     string
	lastSuccessContextErr error
	lastFailureContextErr error
	lastSuccessModel      domain.PublicModel
}

func (s *stubUsageMeter) Reserve(_ context.Context, _ domain.AuthContext, _ string, req domain.GenerateRequest, _ domain.PublicModel, reservationRequestID string) (string, error) {
	s.reserveCalls++
	s.lastReserveReq = req
	s.lastReservationReqID = reservationRequestID
	if s.reserveErr != nil {
		return "", s.reserveErr
	}
	return "reservation-1", nil
}

func (s *stubUsageMeter) RecordSuccess(ctx context.Context, reservationID string, _ domain.AuthContext, _ string, req domain.GenerateRequest, _ domain.GenerateResult, model domain.PublicModel, _ int64) error {
	s.successCalls++
	s.lastReservationID = reservationID
	s.lastSuccessContextErr = ctx.Err()
	s.lastSuccessReq = req
	s.lastSuccessModel = model
	return nil
}

func (s *stubUsageMeter) RecordFailure(ctx context.Context, reservationID *string, _ *domain.AuthContext, _ string, _ *domain.GenerateRequest, _ *domain.PublicModel, _ error, _ *domain.Usage, _ int64) error {
	s.failureCalls++
	if reservationID != nil {
		s.lastReservationID = *reservationID
	}
	s.lastFailureContextErr = ctx.Err()
	return nil
}

type stubProviderRegistry struct {
	provider  *stubProvider
	providers map[string]ports.Provider
	err       error
}

func (s *stubProviderRegistry) GetProvider(name string) (ports.Provider, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.providers != nil {
		provider, ok := s.providers[name]
		if !ok {
			return nil, domain.ErrProviderUnavailable(name)
		}
		return provider, nil
	}
	return s.provider, nil
}

// stubProvider returns canned provider bodies.
type stubProvider struct {
	beforeReturn func()
	err          error
	json         string // Complete's body; a short answer when empty.
	sse          string // Stream's body.
	calls        int
	lastRaw      string
}

const stubAnswer = `{"id":"provider-response","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func (s *stubProvider) Complete(_ context.Context, raw []byte, _ domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error) {
	s.calls++
	s.lastRaw = string(raw)
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	if s.err != nil {
		return nil, nil, s.err
	}
	if s.json == "" {
		return []byte(stubAnswer), nil, nil
	}
	return []byte(s.json), nil, nil
}

func (s *stubProvider) Stream(_ context.Context, raw []byte, _ domain.PublicModel) (ports.ProviderStream, error) {
	s.calls++
	s.lastRaw = string(raw)
	if s.err != nil {
		return ports.ProviderStream{}, s.err
	}
	return ports.ProviderStream{Body: io.NopCloser(strings.NewReader(s.sse))}, nil
}

func TestProxy_FinalizesSuccessAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), middleware.RequestIDKey, "client-request-id"))
	meter := &stubUsageMeter{}
	svc := newDirectModelGenerateService(meter, &stubProvider{beforeReturn: cancel})

	_, err := svc.Proxy(ctx, domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "model",
	}, "sk-test")
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if meter.successCalls != 1 || meter.lastReservationID != "reservation-1" {
		t.Fatalf("success finalization = %d for %q, want one call for reservation-1", meter.successCalls, meter.lastReservationID)
	}
	if meter.lastReservationReqID == "client-request-id" {
		t.Fatal("reservation request ID reused the client-controlled HTTP request ID")
	}
	if _, err := uuid.Parse(meter.lastReservationReqID); err != nil {
		t.Fatalf("reservation request ID = %q, want an internal UUID: %v", meter.lastReservationReqID, err)
	}
	if meter.lastSuccessContextErr != nil {
		t.Fatalf("finalization context error = %v, want nil", meter.lastSuccessContextErr)
	}
}

func TestProxy_ReleasesReservationAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	meter := &stubUsageMeter{}
	svc := newDirectModelGenerateService(meter, &stubProvider{
		beforeReturn: cancel,
		err:          domain.ErrProviderUnavailable("openai"),
	})

	_, err := svc.Proxy(ctx, domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "model",
	}, "sk-test")
	if err == nil {
		t.Fatal("expected provider error")
	}
	if meter.failureCalls != 1 || meter.lastReservationID != "reservation-1" {
		t.Fatalf("failure finalization = %d for %q, want one call for reservation-1", meter.failureCalls, meter.lastReservationID)
	}
	if meter.lastFailureContextErr != nil {
		t.Fatalf("finalization context error = %v, want nil", meter.lastFailureContextErr)
	}
}

func TestProxy_UsesConfiguredFallbackOnce(t *testing.T) {
	meter := &stubUsageMeter{}
	primary := &stubProvider{err: domain.ErrProviderUnavailable("primary")}
	fallback := &stubProvider{}
	svc := newFallbackGenerateService(meter, primary, fallback)

	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "model",
	}, "sk-test")
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("generate calls = primary %d, fallback %d; want 1 each", primary.calls, fallback.calls)
	}
	if meter.reserveCalls != 1 || meter.successCalls != 1 || meter.failureCalls != 0 {
		t.Fatalf("metering calls = reserve %d, success %d, failure %d", meter.reserveCalls, meter.successCalls, meter.failureCalls)
	}
	if call.Model.ProviderConfig.ProviderName != "fallback" || call.Model.ProviderModelID != "fallback-model" {
		t.Fatalf("result target = %s/%s, want fallback/fallback-model", call.Model.ProviderConfig.ProviderName, call.Model.ProviderModelID)
	}
	if meter.lastSuccessModel.ProviderConfig.ProviderName != "fallback" {
		t.Fatalf("metered provider = %q, want fallback", meter.lastSuccessModel.ProviderConfig.ProviderName)
	}
}

func TestProxy_ReturnsFallbackFailure(t *testing.T) {
	meter := &stubUsageMeter{}
	primary := &stubProvider{err: domain.ErrProviderUnavailable("primary")}
	fallbackErr := domain.ErrProviderTimeout("fallback")
	fallback := &stubProvider{err: fallbackErr}
	svc := newFallbackGenerateService(meter, primary, fallback)

	_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "model",
	}, "sk-test")
	if err != fallbackErr {
		t.Fatalf("error = %v, want fallback error %v", err, fallbackErr)
	}
	if primary.calls != 1 || fallback.calls != 1 || meter.failureCalls != 1 {
		t.Fatalf("calls = primary %d, fallback %d, failures %d", primary.calls, fallback.calls, meter.failureCalls)
	}
}

func TestProxy_DoesNotFallbackAfterCancellation(t *testing.T) {
	meter := &stubUsageMeter{}
	primary := &stubProvider{err: domain.ErrClientCanceled()}
	fallback := &stubProvider{}
	svc := newFallbackGenerateService(meter, primary, fallback)

	_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "model",
	}, "sk-test")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls)
	}
}

func newDirectModelGenerateService(meter *stubUsageMeter, provider *stubProvider) *GenerateService {
	return NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
			APIKey:  domain.APIKey{ID: "key1", Active: true},
		}},
		&stubModelCatalog{model: domain.PublicModel{
			PublicModelID:           "model",
			ProviderModelID:         "provider-model",
			ProviderConfig:          domain.ProviderConfig{ProviderName: "openai"},
			SupportsChatCompletions: true,
			MaxContextWindow:        1000,
			MaxOutputTokens:         100,
		}},
		nil,
		&stubProviderRegistry{provider: provider},
		meter,
		nil,
		stubLogger{},
	)
}

func newFallbackGenerateService(meter *stubUsageMeter, primary, fallback *stubProvider) *GenerateService {
	return NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
			APIKey:  domain.APIKey{ID: "key1", Active: true},
		}},
		&stubModelCatalog{model: domain.PublicModel{
			PublicModelID:                 "model",
			ProviderModelID:               "primary-model",
			UpstreamModelName:             "primary-upstream",
			ProviderConfig:                domain.ProviderConfig{ProviderName: "primary"},
			Fallback:                      &domain.ProviderTarget{ProviderModelID: "fallback-model", UpstreamModelName: "fallback-upstream", ProviderConfig: domain.ProviderConfig{ProviderName: "fallback"}},
			SupportsChatCompletions:       true,
			SupportsChatCompletionsStream: true,
			MaxContextWindow:              1000,
			MaxOutputTokens:               100,
		}},
		nil,
		&stubProviderRegistry{providers: map[string]ports.Provider{
			"primary":  primary,
			"fallback": fallback,
		}},
		meter,
		nil,
		stubLogger{},
	)
}

type stubLogger struct{}

func (stubLogger) Debug(string, ...any) {}
func (stubLogger) Info(string, ...any)  {}
func (stubLogger) Warn(string, ...any)  {}
func (stubLogger) Error(string, ...any) {}

func TestProxy_RejectsWhenBalanceIsEmpty(t *testing.T) {
	usage := &stubUsageMeter{reserveErr: domain.ErrInsufficientBalance()}
	svc := NewGenerateService(
		&stubAuthService{
			authCtx: domain.AuthContext{
				Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
				APIKey:  domain.APIKey{ID: "key1", Active: true},
			},
		},
		&stubModelCatalog{
			model: domain.PublicModel{
				PublicModelID:           "openai/gpt-4.1-mini",
				SupportsChatCompletions: true,
				MaxContextWindow:        100000,
				MaxOutputTokens:         16384,
				InputPricePerMillion:    decimal.NewFromFloat(0.75),
				OutputPricePerMillion:   decimal.NewFromFloat(4.50),
			},
		},
		nil,
		nil,
		usage,
		nil, // pii filter
		stubLogger{},
	)

	_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "openai/gpt-4.1-mini",
	}, "sk-test")
	if err == nil {
		t.Fatal("expected insufficient balance error")
	}
	gwErr, ok := err.(*domain.GatewayError)
	if !ok {
		t.Fatalf("expected gateway error, got %T", err)
	}
	if gwErr.Code != domain.ErrCodeInsufficientBalance {
		t.Fatalf("error code = %s, want %s", gwErr.Code, domain.ErrCodeInsufficientBalance)
	}
	if usage.failureCalls != 0 {
		t.Fatalf("failure calls = %d, want 0", usage.failureCalls)
	}
}

func TestProxy_DoesNotRouteUnknownModel(t *testing.T) {
	router := &stubRouterClient{decision: domain.RouteDecision{PublicModelID: "minimax/minimax-m2.7"}}
	svc := NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
			APIKey:  domain.APIKey{ID: "key1", Active: true},
		}},
		&stubModelCatalog{models: map[string]domain.PublicModel{}},
		router,
		nil,
		&stubUsageMeter{},
		nil, // pii filter
		stubLogger{},
	)

	_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "unknown/model",
	}, "sk-test")
	if err == nil {
		t.Fatal("expected unsupported model error")
	}
	gwErr, ok := err.(*domain.GatewayError)
	if !ok || gwErr.Code != domain.ErrCodeUnsupportedModel {
		t.Fatalf("error = %v, want unsupported_model", err)
	}
	if router.calls != 0 {
		t.Fatalf("router calls = %d, want 0", router.calls)
	}
}

func TestProxy_RoutesExplicitRouterToSelectedModel(t *testing.T) {
	usage := &stubUsageMeter{}
	category := "long-context"
	reason := "embedding_matched"
	score := float32(0.456)
	categoryScores := []domain.RoutingCategoryScore{
		{Category: "long-context", Score: score, Threshold: 0.30, PassedThreshold: true, Selected: true},
		{Category: "fast", Score: 0.111, Threshold: 0.30, PassedThreshold: false, Selected: false},
	}
	router := &stubRouterClient{decision: domain.RouteDecision{
		PublicModelID:  "minimax/minimax-m2.7",
		Category:       &category,
		Score:          &score,
		CategoryScores: categoryScores,
		FallbackUsed:   false,
		Reason:         reason,
	}}
	selected := domain.PublicModel{
		PublicModelID:                 "minimax/minimax-m2.7",
		DisplayName:                   "MiniMax M2.7",
		ProviderModelID:               "novita-minimax-m2.7",
		ProviderConfig:                domain.ProviderConfig{ProviderName: "novita"},
		SupportsChatCompletions:       true,
		SupportsChatCompletionsStream: true,
		MaxContextWindow:              1000,
		MaxOutputTokens:               100,
		InputPricePerMillion:          decimal.NewFromFloat(0.03),
		OutputPricePerMillion:         decimal.NewFromFloat(0.12),
	}
	svc := NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
			APIKey:  domain.APIKey{ID: "key1", Active: true},
		}},
		&stubModelCatalog{
			models: map[string]domain.PublicModel{
				"minimax/minimax-m2.7": selected,
			},
			routers: map[string]domain.RouterEntry{
				"dappnode/router": {RouterID: "dappnode/router", Active: true},
			},
		},
		router,
		&stubProviderRegistry{provider: &stubProvider{}},
		usage,
		nil, // pii filter
		stubLogger{},
	)

	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "dappnode/router",
	}, "sk-test")
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if router.calls != 1 {
		t.Fatalf("router calls = %d, want 1", router.calls)
	}
	if call.Model.PublicModelID != "minimax/minimax-m2.7" || !strings.Contains(string(call.Body), `"model":"minimax/minimax-m2.7"`) {
		t.Fatalf("result model = %q, want routed model", call.Model.PublicModelID)
	}
	if usage.lastSuccessReq.RequestedModelID != "dappnode/router" {
		t.Fatalf("requested model = %q, want router id", usage.lastSuccessReq.RequestedModelID)
	}
	if usage.lastSuccessReq.RouterID == nil || *usage.lastSuccessReq.RouterID != "dappnode/router" {
		t.Fatalf("router id = %#v, want dappnode/router", usage.lastSuccessReq.RouterID)
	}
	if usage.lastSuccessReq.RoutedPublicModelID == nil || *usage.lastSuccessReq.RoutedPublicModelID != "minimax/minimax-m2.7" {
		t.Fatalf("routed public model = %#v, want minimax/minimax-m2.7", usage.lastSuccessReq.RoutedPublicModelID)
	}
	if usage.lastSuccessReq.MatchedCategory == nil || *usage.lastSuccessReq.MatchedCategory != category {
		t.Fatalf("matched category = %#v, want %s", usage.lastSuccessReq.MatchedCategory, category)
	}
	if usage.lastSuccessReq.RoutingScore == nil || *usage.lastSuccessReq.RoutingScore != score {
		t.Fatalf("routing score = %#v, want %v", usage.lastSuccessReq.RoutingScore, score)
	}
	if len(usage.lastSuccessReq.RoutingCategoryScores) != len(categoryScores) {
		t.Fatalf("category scores len = %d, want %d", len(usage.lastSuccessReq.RoutingCategoryScores), len(categoryScores))
	}
	if usage.lastSuccessReq.RoutingCategoryScores[0] != categoryScores[0] || usage.lastSuccessReq.RoutingCategoryScores[1] != categoryScores[1] {
		t.Fatalf("category scores = %#v, want %#v", usage.lastSuccessReq.RoutingCategoryScores, categoryScores)
	}
	if usage.lastSuccessReq.DecisionReason == nil || *usage.lastSuccessReq.DecisionReason != reason {
		t.Fatalf("decision reason = %#v, want %s", usage.lastSuccessReq.DecisionReason, reason)
	}
	if usage.lastSuccessReq.FallbackUsed == nil || *usage.lastSuccessReq.FallbackUsed {
		t.Fatalf("fallback used = %#v, want false", usage.lastSuccessReq.FallbackUsed)
	}
	if usage.reserveCalls != 1 {
		t.Fatalf("reserve calls = %d, want 1", usage.reserveCalls)
	}
	if usage.lastReserveReq.RouterID == nil || *usage.lastReserveReq.RouterID != "dappnode/router" {
		t.Fatalf("reserve router id = %#v, want dappnode/router", usage.lastReserveReq.RouterID)
	}
}

func TestProxy_ReturnsRouterErrorForExplicitRouterOutage(t *testing.T) {
	router := &stubRouterClient{err: domain.ErrInternal("an internal error occurred").WithMeta("dependency", "router")}
	svc := NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
			APIKey:  domain.APIKey{ID: "key1", Active: true},
		}},
		&stubModelCatalog{
			models: map[string]domain.PublicModel{},
			routers: map[string]domain.RouterEntry{
				"dappnode/router": {RouterID: "dappnode/router", Active: true},
			},
		},
		router,
		nil,
		&stubUsageMeter{},
		nil, // pii filter
		stubLogger{},
	)

	_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{
		PublicModelID: "dappnode/router",
	}, "sk-test")
	if err == nil {
		t.Fatal("expected router outage error")
	}
	gwErr, ok := err.(*domain.GatewayError)
	if !ok || gwErr.Code != domain.ErrCodeInternalError || gwErr.Type != domain.ErrTypeInternal {
		t.Fatalf("error = %v, want internal_error", err)
	}
	if router.calls != 1 {
		t.Fatalf("router calls = %d, want 1", router.calls)
	}
}
