package services

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func newGuestTestService(meter *stubUsageMeter, provider *stubProvider, isGuest bool) *GenerateService {
	svc := newDirectModelGenerateService(meter, provider)
	svc.auth = &stubAuthService{authCtx: domain.AuthContext{
		Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive, IsGuest: isGuest},
		APIKey:  domain.APIKey{ID: "key1", Active: true},
	}}
	svc.SetGuestMaxOutputTokens(50)
	return svc
}

func proxyChat(t *testing.T, svc *GenerateService, body string) {
	t.Helper()
	req, err := mapper.ChatCompletionRequestToDomain([]byte(body))
	if err != nil {
		t.Fatalf("map request: %v", err)
	}
	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(body), req, "sk-test"); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
}

func sentField(t *testing.T, raw, field string) any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("provider body: %v", err)
	}
	return body[field]
}

// A guest's request always reaches the provider with an output limit no
// larger than the guest limit, and Metering holds credit for that limit.
func TestProxy_GuestRequestsCarryTheOutputLimit(t *testing.T) {
	const user = `"messages":[{"role":"user","content":"hi"}]`
	for name, tc := range map[string]struct {
		body, field string
		want        float64
	}{
		"no limit":                   {`{"model":"model",` + user + `}`, "max_tokens", 50},
		"larger limit":               {`{"model":"model",` + user + `,"max_tokens":4000}`, "max_tokens", 50},
		"smaller limit is kept":      {`{"model":"model",` + user + `,"max_tokens":20}`, "max_tokens", 20},
		"max_completion_tokens name": {`{"model":"model",` + user + `,"max_completion_tokens":4000}`, "max_completion_tokens", 50},
	} {
		t.Run(name, func(t *testing.T) {
			meter, provider := &stubUsageMeter{}, &stubProvider{}
			proxyChat(t, newGuestTestService(meter, provider, true), tc.body)
			if got := sentField(t, provider.lastRaw, tc.field); got != tc.want {
				t.Fatalf("provider got %s = %v, want %v", tc.field, got, tc.want)
			}
			if got := meter.lastReserveReq.MaxOutputTokens; got == nil || float64(*got) != tc.want {
				t.Fatalf("held output tokens = %v, want %v", got, tc.want)
			}
		})
	}
}

// Apart from the output limit, a guest's request is an ordinary request:
// other fields reach the provider as sent, and signed-in accounts get no
// limit added.
func TestProxy_GuestRequestsAreOtherwiseOrdinary(t *testing.T) {
	body := `{"model":"model","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`

	meter, provider := &stubUsageMeter{}, &stubProvider{}
	proxyChat(t, newGuestTestService(meter, provider, true), body)
	if sentField(t, provider.lastRaw, "tools") == nil {
		t.Fatal("a guest's tools were dropped")
	}

	meter, provider = &stubUsageMeter{}, &stubProvider{}
	proxyChat(t, newGuestTestService(meter, provider, false), body)
	if got := sentField(t, provider.lastRaw, "max_tokens"); got != nil {
		t.Fatalf("a signed-in request got max_tokens %v", got)
	}
	if meter.lastReserveReq.MaxOutputTokens != nil {
		t.Fatalf("a signed-in request's hold got an output limit: %d", *meter.lastReserveReq.MaxOutputTokens)
	}
}
