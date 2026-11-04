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

// Every chat request reaches the provider with an output limit no larger than
// its account's limit, and Metering holds credit for that limit: 32,768 for
// accounts, the guest limit (50 here) for guests.
func TestProxy_ChatRequestsCarryTheOutputLimit(t *testing.T) {
	const user = `"messages":[{"role":"user","content":"hi"}]`
	for name, tc := range map[string]struct {
		guest       bool
		body, field string
		want        float64
	}{
		"guest, no limit":                {true, `{"model":"model",` + user + `}`, "max_completion_tokens", 50},
		"guest, larger limit":            {true, `{"model":"model",` + user + `,"max_tokens":4000}`, "max_tokens", 50},
		"guest, smaller limit is kept":   {true, `{"model":"model",` + user + `,"max_tokens":20}`, "max_tokens", 20},
		"guest, max_completion_tokens":   {true, `{"model":"model",` + user + `,"max_completion_tokens":4000}`, "max_completion_tokens", 50},
		"account, no limit":              {false, `{"model":"model",` + user + `}`, "max_completion_tokens", DefaultMaxOutputTokens},
		"account, larger limit":          {false, `{"model":"model",` + user + `,"max_tokens":100000}`, "max_tokens", DefaultMaxOutputTokens},
		"account, smaller limit is kept": {false, `{"model":"model",` + user + `,"max_tokens":80}`, "max_tokens", 80},
	} {
		t.Run(name, func(t *testing.T) {
			meter, provider := &stubUsageMeter{}, &stubProvider{}
			proxyChat(t, newGuestTestService(meter, provider, tc.guest), tc.body)
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
// other fields reach the provider as sent.
func TestProxy_GuestRequestsAreOtherwiseOrdinary(t *testing.T) {
	body := `{"model":"model","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
	meter, provider := &stubUsageMeter{}, &stubProvider{}
	proxyChat(t, newGuestTestService(meter, provider, true), body)
	if sentField(t, provider.lastRaw, "tools") == nil {
		t.Fatal("a guest's tools were dropped")
	}
}
