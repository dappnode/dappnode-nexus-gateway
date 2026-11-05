package services

import (
	"context"
	"errors"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// A guest key reaches only models ticked for guests. Anything else is refused
// before routing, PII masking, Metering or a provider runs: a router can call
// a paid API, and a free model would cost nothing against the guest's credit.
func TestProxy_GuestKeysCallOnlyGuestModels(t *testing.T) {
	guestModel := domain.PublicModel{
		PublicModelID: "flash", ProviderModelID: "flash", ProviderConfig: domain.ProviderConfig{ProviderName: "openai"},
		SupportsChatCompletions: true, MaxContextWindow: 1000, MaxOutputTokens: 100, GuestAllowed: true,
	}
	freeModel := guestModel
	freeModel.PublicModelID, freeModel.GuestAllowed = "free", false

	for name, tc := range map[string]struct {
		endpoint, model string
		wantErr         string
	}{
		"ticked model":           {domain.EndpointChatCompletions, "flash", ""},
		"model not ticked":       {domain.EndpointChatCompletions, "free", domain.ErrCodeGuestModelNotAllowed},
		"router":                 {domain.EndpointChatCompletions, "nexus/auto", domain.ErrCodeGuestModelNotAllowed},
		"other endpoint":         {domain.EndpointSystemOne, "flash", domain.ErrCodeGuestModelNotAllowed},
		"unknown model or route": {domain.EndpointChatCompletions, "nope", domain.ErrCodeGuestModelNotAllowed},
	} {
		t.Run(name, func(t *testing.T) {
			meter, provider, router := &stubUsageMeter{}, &stubProvider{}, &stubRouterClient{
				decision: domain.RouteDecision{PublicModelID: "flash"},
			}
			svc := newGuestTestService(meter, provider, true)
			svc.catalog = &stubModelCatalog{
				models:  map[string]domain.PublicModel{"flash": guestModel, "free": freeModel},
				routers: map[string]domain.RouterEntry{"nexus/auto": {RouterID: "nexus/auto"}},
			}
			svc.router = router
			body := `{"model":"` + tc.model + `","messages":[{"role":"user","content":"hi"}]}`
			req, err := mapper.ChatCompletionRequestToDomain([]byte(body))
			if err != nil {
				t.Fatalf("map request: %v", err)
			}
			_, err = svc.Proxy(context.Background(), tc.endpoint, []byte(body), req, "sk-test")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Proxy: %v", err)
				}
				return
			}
			var gwErr *domain.GatewayError
			if !errors.As(err, &gwErr) || gwErr.Code != tc.wantErr || gwErr.HTTPStatus != 403 {
				t.Fatalf("err = %v, want 403 %s", err, tc.wantErr)
			}
			if router.calls != 0 || meter.reserveCalls != 0 || provider.calls != 0 {
				t.Fatalf("router %d, reserve %d, provider %d calls; want none", router.calls, meter.reserveCalls, provider.calls)
			}
		})
	}
}

// When the catalog can't answer, a guest gets the catalog's error, not a
// misleading "not allowed".
func TestProxy_GuestModelCheckPassesCatalogErrors(t *testing.T) {
	meter, provider := &stubUsageMeter{}, &stubProvider{}
	svc := newGuestTestService(meter, provider, true)
	svc.catalog = &stubModelCatalog{err: domain.ErrInternal("metering unavailable")}
	body := `{"model":"flash","messages":[{"role":"user","content":"hi"}]}`
	req, _ := mapper.ChatCompletionRequestToDomain([]byte(body))
	_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(body), req, "sk-test")
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeInternalError {
		t.Fatalf("err = %v, want internal_error", err)
	}
}
