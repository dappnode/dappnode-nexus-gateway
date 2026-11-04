package services

import (
	"context"
	"io"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// hangUpProvider records the context the gateway gives it. Its stream comes
// from a pipe the test writes to, closed like an HTTP body when that context
// is cancelled.
type hangUpProvider struct {
	onCall func() // Called before the provider answers.
	errAt  error  // The context's error after onCall.
	ctx    context.Context
	body   *io.PipeReader
	write  *io.PipeWriter
}

func newHangUpProvider(onCall func()) *hangUpProvider {
	pr, pw := io.Pipe()
	return &hangUpProvider{onCall: onCall, body: pr, write: pw}
}

func (p *hangUpProvider) call(ctx context.Context) {
	p.ctx = ctx
	if p.onCall != nil {
		p.onCall()
	}
	p.errAt = ctx.Err()
}

func (p *hangUpProvider) Complete(ctx context.Context, _ []byte, _ domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error) {
	p.call(ctx)
	return []byte(stubAnswer), nil, nil
}

func (p *hangUpProvider) Stream(ctx context.Context, _ []byte, _ domain.PublicModel) (ports.ProviderStream, error) {
	p.call(ctx)
	context.AfterFunc(ctx, func() { _ = p.write.CloseWithError(ctx.Err()) })
	go func() {
		_, _ = io.WriteString(p.write, "data: {\"id\":\"r\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hel\"}}]}\n\n")
	}()
	return ports.ProviderStream{Body: p.body}, nil
}

func newHangUpService(meter *stubUsageMeter, provider ports.Provider) *GenerateService {
	svc := newDirectModelGenerateService(meter, &stubProvider{})
	svc.registry = &stubProviderRegistry{providers: map[string]ports.Provider{"openai": provider}}
	return svc
}

func drain(stream *ProxyStream) {
	for {
		if _, err := stream.Next(); err != nil {
			break
		}
	}
	_ = stream.Close()
}

// A client that hangs up mid-stream doesn't stop the provider: the answer
// runs to the end, and the usage it reports is charged.
func TestProxy_ClientThatHangsUpMidStreamIsCharged(t *testing.T) {
	meter := &stubUsageMeter{}
	client, hangUp := context.WithCancel(context.Background())
	provider := newHangUpProvider(nil)
	svc := newHangUpService(meter, provider)

	call, err := svc.Proxy(client, domain.EndpointChatCompletions, []byte(`{"stream":true}`), domain.GenerateRequest{PublicModelID: "model", Stream: true}, "sk-test")
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	hangUp()
	if err := provider.ctx.Err(); err != nil {
		t.Fatalf("the client hanging up cancelled the provider stream: %v", err)
	}
	go func() {
		_, _ = io.WriteString(provider.write, "data: {\"id\":\"r\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"id\":\"r\",\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n"+
			"data: [DONE]\n\n")
		_ = provider.write.Close()
	}()
	drain(call.Stream)
	if meter.successCalls != 1 || meter.failureCalls != 0 {
		t.Fatalf("success %d, failure %d; want the finished answer charged once", meter.successCalls, meter.failureCalls)
	}
}

// The same holds without streaming.
func TestProxy_ClientThatHangsUpDuringACompleteAnswerIsCharged(t *testing.T) {
	meter := &stubUsageMeter{}
	client, hangUp := context.WithCancel(context.Background())
	provider := newHangUpProvider(hangUp)
	svc := newHangUpService(meter, provider)

	if _, err := svc.Proxy(client, domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "model"}, "sk-test"); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if provider.errAt != nil {
		t.Fatalf("the client hanging up cancelled the provider call: %v", provider.errAt)
	}
	if meter.successCalls != 1 {
		t.Fatalf("success %d, want the answer charged once", meter.successCalls)
	}
}

// No fallback answer is generated for a client that has already hung up.
func TestProxy_NoFallbackForAClientThatHungUp(t *testing.T) {
	meter := &stubUsageMeter{}
	client, hangUp := context.WithCancel(context.Background())
	primary := &stubProvider{beforeReturn: hangUp, err: domain.ErrProviderUnavailable("primary")}
	fallback := &stubProvider{}
	svc := newFallbackGenerateService(meter, primary, fallback)

	if _, err := svc.Proxy(client, domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "model"}, "sk-test"); err == nil {
		t.Fatal("expected the primary's error")
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls)
	}
}
