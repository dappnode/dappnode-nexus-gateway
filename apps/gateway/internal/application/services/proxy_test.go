package services

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type proxyStub = stubProvider

// resultMeter records what was metered.
type resultMeter struct {
	stubUsageMeter
	result *domain.GenerateResult
	failed error
}

func (m *resultMeter) RecordSuccess(ctx context.Context, id string, a domain.AuthContext, e string, req domain.GenerateRequest, res domain.GenerateResult, model domain.PublicModel, l int64) error {
	m.result = &res
	return m.stubUsageMeter.RecordSuccess(ctx, id, a, e, req, res, model, l)
}

func (m *resultMeter) RecordFailure(ctx context.Context, id *string, a *domain.AuthContext, e string, req *domain.GenerateRequest, model *domain.PublicModel, err error, u *domain.Usage, l int64) error {
	m.failed = err
	return m.stubUsageMeter.RecordFailure(ctx, id, a, e, req, model, err, u, l)
}

func proxyService(meter *resultMeter, pii string, providers map[string]ports.Provider, fallback bool) *GenerateService {
	model := domain.PublicModel{
		PublicModelID: "zai-org/glm-5.3-flash", ProviderModelID: "pm", UpstreamModelName: "glm-5-3-flash",
		ProviderConfig:          domain.ProviderConfig{ProviderName: "primary"},
		SupportsChatCompletions: true, SupportsChatCompletionsStream: true, SupportsTools: true, SupportsParallelToolCalls: true,
		MaxContextWindow: 1000, MaxOutputTokens: 100,
	}
	if fallback {
		model.Fallback = &domain.ProviderTarget{ProviderModelID: "fm", UpstreamModelName: "glm-fallback", ProviderConfig: domain.ProviderConfig{ProviderName: "fallback"}}
	}
	var filter ports.PIIFilter
	if pii != "" {
		filter = &fakePIIFilter{enabled: true}
	}
	return NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive}, APIKey: domain.APIKey{ID: "key1", Active: true, PIIMode: pii}}},
		&stubModelCatalog{model: model}, nil, &stubProviderRegistry{providers: providers}, meter, filter, stubLogger{},
	)
}

func relayAll(t *testing.T, s *ProxyStream) (string, error) {
	t.Helper()
	var b strings.Builder
	for {
		line, err := s.Next()
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return b.String(), err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
}

func streamRequest() domain.GenerateRequest {
	return domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash", Stream: true}
}

// The provider's stream reaches the client byte for byte (the shapes that
// broke the translating path included), except the model name; usage and
// the finish reason are metered from it.
func TestProxyStream_ForwardsProviderBytes(t *testing.T) {
	upstream := ": OPENROUTER PROCESSING\n\n" +
		`data: {"id":"c1","model":"glm-5-3-flash","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Think <b>&</b>"},"logprobs":null,"finish_reason":null}],"system_fingerprint":"x"}` + "\n\n" +
		`data:{"id":"c1","model":"glm-5-3-flash","choices":[{"index":0,"delta":{"content":"Reading.","tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read","arguments":"{\"p\":1}"}},{"index":1,"id":"b","type":"function","function":{"name":"list","arguments":""}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
		`data: {"id":"c1","model":"glm-5-3-flash","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}}}` + "\n\n" +
		"data: [DONE]\n\n"
	primary := &proxyStub{sse: upstream}
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": primary}, false)
	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{"model":"zai-org/glm-5.3-flash","stream_options":{"include_usage":true}}`), streamRequest(), "key")
	if err != nil {
		t.Fatal(err)
	}
	got, err := relayAll(t, call.Stream)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(upstream, `"model":"glm-5-3-flash"`, `"model":"zai-org/glm-5.3-flash"`)
	if got != want {
		t.Fatalf("relayed stream differs:\n got %q\nwant %q", got, want)
	}
	call.Stream.Close()
	if meter.result == nil || meter.result.Usage == nil || meter.result.Usage.PromptTokens != 10 || meter.result.Usage.CacheReadTokens != 4 ||
		meter.result.FinishReason == nil || *meter.result.FinishReason != "tool_calls" || meter.result.ID != "c1" || meter.successCalls != 1 || meter.failureCalls != 0 {
		t.Fatalf("metering: %+v success=%d failure=%d", meter.result, meter.successCalls, meter.failureCalls)
	}
}

func TestProxyStream_FallsBackBeforeFirstOutput(t *testing.T) {
	primary := &proxyStub{sse: "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n"}
	fallback := &proxyStub{sse: "data: {\"id\":\"y\",\"model\":\"glm-fallback\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"}
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": primary, "fallback": fallback}, true)
	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), streamRequest(), "key")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := relayAll(t, call.Stream)
	if strings.Contains(got, "overloaded") || !strings.Contains(got, `"content":"hi"`) || call.Model.ProviderConfig.ProviderName != "fallback" {
		t.Fatalf("fallback not used cleanly: %q", got)
	}
	if meter.successCalls != 1 {
		t.Fatalf("success=%d", meter.successCalls)
	}
}

func TestProxyStream_ErrorAfterOutputIsForwardedAndNotBilledAsSuccess(t *testing.T) {
	primary := &proxyStub{sse: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n"}
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": primary}, false)
	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), streamRequest(), "key")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := relayAll(t, call.Stream)
	if !strings.Contains(got, "overloaded") || call.Stream.Complete() {
		t.Fatalf("error not relayed: %q", got)
	}
	if meter.failed == nil || meter.successCalls != 0 {
		t.Fatalf("failure not recorded")
	}
}

func TestProxyStream_EmptyStreamIsAProviderError(t *testing.T) {
	primary := &proxyStub{sse: ""}
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": primary}, false)
	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), streamRequest(), "key"); err == nil {
		t.Fatal("empty stream accepted")
	}
	if meter.failureCalls != 1 {
		t.Fatalf("reservation not released")
	}
}

func TestProxyStream_ClientLeaving(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
	for name, lines := range map[string]int{"before finish": 1, "after finish": 3} {
		t.Run(name, func(t *testing.T) {
			meter := &resultMeter{}
			svc := proxyService(meter, "", map[string]ports.Provider{"primary": &proxyStub{sse: body}}, false)
			call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), streamRequest(), "key")
			if err != nil {
				t.Fatal(err)
			}
			for range lines {
				call.Stream.Next()
			}
			call.Stream.Close()
			if name == "before finish" && (meter.failed == nil || meter.successCalls != 0) {
				t.Fatal("abandoned stream billed as success")
			}
			if name == "after finish" && (meter.successCalls != 1 || meter.result.Usage == nil) {
				t.Fatal("finished stream not billed")
			}
		})
	}
}

func TestProxyJSON(t *testing.T) {
	primary := &proxyStub{json: `{"id":"r1","model":"glm-5-3-flash","choices":[{"message":{"role":"assistant","content":"hi","reasoning_content":"why"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`}
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": primary}, false)
	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(call.Body), `"model":"zai-org/glm-5.3-flash"`) || !strings.Contains(string(call.Body), `"reasoning_content":"why"`) {
		t.Fatalf("body %s", call.Body)
	}
	if meter.result == nil || meter.result.Usage.PromptTokens != 2 {
		t.Fatal("usage not metered")
	}
	primary.json = `{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`
	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key"); err == nil {
		t.Fatal("error body returned as success")
	}
}

// With the PII filter off (as in production), a key's PII mode masks
// nothing, so its requests are proxied too.
func TestProxy_PIIModeWithFilterOffProxies(t *testing.T) {
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": &proxyStub{json: `{"id":"r","choices":[]}`}}, false)
	svc.auth = &stubAuthService{authCtx: domain.AuthContext{Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive}, APIKey: domain.APIKey{ID: "key1", Active: true, PIIMode: "high"}}}
	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key"); err != nil {
		t.Fatalf("not proxied: %v", err)
	}
}

// The gateway asks every provider for usage, to meter it; a client that
// didn't ask gets the stream it would get from OpenAI, without that event.
func TestProxyStream_UnrequestedUsageIsMeteredNotForwarded(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": &proxyStub{sse: body}}, false)
	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{"stream":true}`), streamRequest(), "key")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := relayAll(t, call.Stream)
	want := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if meter.result == nil || meter.result.Usage == nil || meter.result.Usage.PromptTokens != 3 {
		t.Fatal("usage not metered")
	}
}
