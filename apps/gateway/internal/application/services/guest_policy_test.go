package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

var testGuestPolicy = GuestPolicy{
	Models:          map[string]bool{"guest-model": true},
	MaxRequestBytes: 2048,
	MaxOutputTokens: 512,
}

// applyGuest maps a body the way the chat handler does, then applies the
// guest policy to it.
func applyGuest(t *testing.T, policy GuestPolicy, endpoint, body string) ([]byte, domain.GenerateRequest, error) {
	t.Helper()
	req, err := mapper.ChatCompletionRequestToDomain([]byte(body))
	if err != nil {
		t.Fatalf("map request: %v", err)
	}
	return policy.Apply(endpoint, []byte(body), req)
}

func decodeBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode rewritten body: %v", err)
	}
	return body
}

func TestGuestPolicy_WritesTheOutputLimitIntoTheRequest(t *testing.T) {
	cases := []struct {
		name, body, field string
		want              int
	}{
		{"no limit sent", `{"model":"guest-model","messages":[{"role":"user","content":"hi"}]}`, "max_tokens", 512},
		{"higher limit is clamped", `{"model":"guest-model","messages":[{"role":"user","content":"hi"}],"max_tokens":100000}`, "max_tokens", 512},
		{"lower limit is kept", `{"model":"guest-model","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`, "max_tokens", 64},
		{"zero limit means the cap", `{"model":"guest-model","messages":[{"role":"user","content":"hi"}],"max_tokens":0}`, "max_tokens", 512},
		{"client's field name is kept", `{"model":"guest-model","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":9000}`, "max_completion_tokens", 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, req, err := applyGuest(t, testGuestPolicy, domain.EndpointChatCompletions, tc.body)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			body := decodeBody(t, raw)
			if got, _ := body[tc.field].(float64); int(got) != tc.want {
				t.Fatalf("%s in body = %v, want %d", tc.field, body[tc.field], tc.want)
			}
			if req.MaxOutputTokens == nil || *req.MaxOutputTokens != tc.want {
				t.Fatalf("request summary limit = %v, want %d", req.MaxOutputTokens, tc.want)
			}
			if _, both := body["max_tokens"]; both && tc.field == "max_completion_tokens" {
				t.Fatal("the body carries both output-limit fields")
			}
		})
	}
}

func TestGuestPolicy_AcceptsPlainTextChat(t *testing.T) {
	body := `{"model":"guest-model","stream":true,"stream_options":{"include_usage":true},"temperature":1,
		"messages":[{"role":"system","content":"You are Nexus."},{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":"hello"},{"role":"user","content":"again"}]}`
	if _, _, err := applyGuest(t, testGuestPolicy, domain.EndpointChatCompletions, body); err != nil {
		t.Fatalf("plain text chat refused: %v", err)
	}
}

// Everything that could raise a guest request's cost, or reach a model or
// feature the reservation does not cover, is refused.
func TestGuestPolicy_RefusesWhatGuestsCannotUse(t *testing.T) {
	user := `{"role":"user","content":"hi"}`
	cases := map[string]struct {
		endpoint, body, code string
	}{
		"other model":          {domain.EndpointChatCompletions, `{"model":"pricey-model","messages":[` + user + `]}`, domain.ErrCodeGuestNotAllowed},
		"router":               {domain.EndpointChatCompletions, `{"model":"nexus/auto","messages":[` + user + `]}`, domain.ErrCodeGuestNotAllowed},
		"other endpoint":       {domain.EndpointSystemOne, `{"model":"guest-model","messages":[` + user + `]}`, domain.ErrCodeGuestNotAllowed},
		"reasoning option":     {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[` + user + `],"reasoning_effort":"high"}`, domain.ErrCodeGuestNotAllowed},
		"provider thinking":    {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[` + user + `],"thinking":{"type":"enabled"}}`, domain.ErrCodeGuestNotAllowed},
		"provider extra body":  {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[` + user + `],"extra_body":{}}`, domain.ErrCodeGuestNotAllowed},
		"tools":                {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f"}}]}`, domain.ErrCodeGuestNotAllowed},
		"tool role":            {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[` + user + `,{"role":"tool","tool_call_id":"1","content":"x"}]}`, domain.ErrCodeGuestNotAllowed},
		"developer role":       {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[{"role":"developer","content":"x"}]}`, domain.ErrCodeGuestNotAllowed},
		"image":                {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x"}}]}]}`, domain.ErrCodeGuestNotAllowed},
		"text part extra":      {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{}}]}]}`, domain.ErrCodeGuestNotAllowed},
		"message extra field":  {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[{"role":"user","content":"x","reasoning_content":"y"}]}`, domain.ErrCodeGuestNotAllowed},
		"conversation too big": {domain.EndpointChatCompletions, `{"model":"guest-model","messages":[{"role":"user","content":"` + strings.Repeat("a", 2048) + `"}]}`, domain.ErrCodeGuestRequestTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := applyGuest(t, testGuestPolicy, tc.endpoint, tc.body)
			var gwErr *domain.GatewayError
			if !errors.As(err, &gwErr) || gwErr.Code != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestGuestPolicy_PromptTokenBoundCoversTheUpstreamBody(t *testing.T) {
	body := []byte(`{"model":"guest-model","messages":[{"role":"user","content":"hi"}]}`)
	if got := testGuestPolicy.PromptTokenBound(body); got != len(body)+guestPromptOverheadTokens {
		t.Fatalf("bound = %d, want %d", got, len(body)+guestPromptOverheadTokens)
	}
}

func newGuestGenerateService(meter *stubUsageMeter, provider *stubProvider, isGuest bool) *GenerateService {
	svc := newDirectModelGenerateService(meter, provider)
	svc.auth = &stubAuthService{authCtx: domain.AuthContext{
		Account: domain.Account{ID: "guest-account", Status: domain.AccountStatusActive, IsGuest: isGuest},
		APIKey:  domain.APIKey{ID: "guest-key", Active: true},
	}}
	return svc
}

func proxyGuestRequest(t *testing.T, svc *GenerateService, body string) error {
	t.Helper()
	req, err := mapper.ChatCompletionRequestToDomain([]byte(body))
	if err != nil {
		t.Fatalf("map request: %v", err)
	}
	_, err = svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(body), req, "sk-guest")
	return err
}

// A guest's reservation covers the prompt actually sent and the capped
// output, and the provider receives the capped request.
func TestProxy_GuestReservationIsBoundedByTheRequest(t *testing.T) {
	meter, provider := &stubUsageMeter{}, &stubProvider{}
	svc := newGuestGenerateService(meter, provider, true)
	svc.SetGuestPolicy(GuestPolicy{Models: map[string]bool{"model": true}, MaxRequestBytes: 4096, MaxOutputTokens: 50})

	if err := proxyGuestRequest(t, svc, `{"model":"model","messages":[{"role":"user","content":"hello"}],"max_tokens":1000}`); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	got := meter.lastReserveReq
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 50 {
		t.Fatalf("reserved output tokens = %v, want 50", got.MaxOutputTokens)
	}
	sent := provider.lastRaw
	if got.MaxInputTokens == nil || *got.MaxInputTokens != len(sent)+guestPromptOverheadTokens {
		t.Fatalf("reserved input tokens = %v, want %d (upstream body %d bytes + overhead)", got.MaxInputTokens, len(sent)+guestPromptOverheadTokens, len(sent))
	}
	if body := decodeBody(t, []byte(sent)); body["max_tokens"] != float64(50) {
		t.Fatalf("provider got max_tokens %v, want 50", body["max_tokens"])
	}
}

// A refused guest request never reserves credit or reaches a provider.
func TestProxy_RefusedGuestRequestReachesNothing(t *testing.T) {
	meter, provider := &stubUsageMeter{}, &stubProvider{}
	svc := newGuestGenerateService(meter, provider, true)
	svc.SetGuestPolicy(GuestPolicy{Models: map[string]bool{"model": true}, MaxRequestBytes: 4096, MaxOutputTokens: 50})

	err := proxyGuestRequest(t, svc, `{"model":"model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`)
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeGuestNotAllowed {
		t.Fatalf("error = %v, want %s", err, domain.ErrCodeGuestNotAllowed)
	}
	if meter.reserveCalls != 0 || provider.calls != 0 {
		t.Fatalf("refused guest request reserved %d times and called the provider %d times", meter.reserveCalls, provider.calls)
	}
}

// Without a configured policy a guest key is refused, never served unbounded.
func TestProxy_GuestWithoutPolicyIsRefused(t *testing.T) {
	meter, provider := &stubUsageMeter{}, &stubProvider{}
	svc := newGuestGenerateService(meter, provider, true)

	err := proxyGuestRequest(t, svc, `{"model":"model","messages":[{"role":"user","content":"hi"}]}`)
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeGuestNotAllowed {
		t.Fatalf("error = %v, want %s", err, domain.ErrCodeGuestNotAllowed)
	}
	if meter.reserveCalls != 0 || provider.calls != 0 {
		t.Fatal("a guest key was served without a guest policy")
	}
}

// Signed-in accounts are untouched by the guest policy.
func TestProxy_GuestPolicyLeavesOtherAccountsAlone(t *testing.T) {
	meter, provider := &stubUsageMeter{}, &stubProvider{}
	svc := newGuestGenerateService(meter, provider, false)
	svc.SetGuestPolicy(GuestPolicy{Models: map[string]bool{"other": true}, MaxRequestBytes: 10, MaxOutputTokens: 1})

	body := `{"model":"model","messages":[{"role":"user","content":"a request longer than ten bytes"}],"max_tokens":80,"reasoning_effort":"high"}`
	if err := proxyGuestRequest(t, svc, body); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if meter.lastReserveReq.MaxInputTokens != nil {
		t.Fatalf("a signed-in request got an input bound: %d", *meter.lastReserveReq.MaxInputTokens)
	}
	if got := meter.lastReserveReq.MaxOutputTokens; got == nil || *got != 80 {
		t.Fatalf("a signed-in request's output limit changed: %v", got)
	}
}
