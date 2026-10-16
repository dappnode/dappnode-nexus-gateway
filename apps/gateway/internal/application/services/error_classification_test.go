package services

import (
	"context"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A primary provider failure that the fallback covers is counted, though the
// request succeeds and the error metric never sees it.
func TestProxy_CountsFallbacks(t *testing.T) {
	primary := &stubProvider{err: domain.ErrProviderUnavailable("primary")}
	svc := newFallbackGenerateService(&stubUsageMeter{}, primary, &stubProvider{})
	counter := metrics.FallbacksTotal.WithLabelValues(domain.EndpointChatCompletions, "model", "primary", "fallback",
		domain.FaultProvider, domain.ErrCodeProviderUnavailable)
	before := testutil.ToFloat64(counter)

	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "model"}, "sk-test"); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Fatalf("fallbacks_total delta = %v, want 1", got)
	}
}

// A provider name this build doesn't know is Nexus's configuration, not the
// provider's outage, though the client still sees provider_unavailable.
func TestProxy_UnregisteredProviderIsNexusFault(t *testing.T) {
	svc := proxyService(&resultMeter{}, "", map[string]ports.Provider{}, false)
	counter := metrics.ErrorsTotal.WithLabelValues(domain.EndpointChatCompletions, "zai-org/glm-5.3-flash", "primary",
		metrics.PhaseProvider, domain.FaultNexus, domain.ErrCodeProviderUnavailable, "none")
	before := testutil.ToFloat64(counter)

	_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key")
	gwErr, ok := err.(*domain.GatewayError)
	if !ok || gwErr.Code != domain.ErrCodeProviderUnavailable || gwErr.Metadata["cause"] != domain.CauseProviderNotRegistered {
		t.Fatalf("error = %v", err)
	}
	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Fatalf("errors_total delta = %v, want 1", got)
	}
}

// When the client leaves during PII masking, the canceled analyzer call is
// the client's cancellation, not a Nexus failure to fix.
func TestProxy_ClientLeavingDuringPIIIsNotNexusFault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc, _ := piiService(&fakePIIFilter{enabled: true, err: context.Canceled}, domain.APIKeyPIIModeHigh, &stubProvider{})

	_, err := svc.Proxy(ctx, domain.EndpointChatCompletions, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key")
	gwErr, ok := err.(*domain.GatewayError)
	if !ok || gwErr.Code != domain.ErrCodeClientCanceled || domain.FaultOf(err) != domain.FaultCaller {
		t.Fatalf("error = %v, want client_canceled", err)
	}
}

func TestClientGoneKeepsCallerAndProviderErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range []error{domain.ErrUnsupportedModel("m"), domain.ErrInsufficientBalance(), domain.ErrProviderTimeout("p")} {
		if got := clientGone(ctx, err); got != err {
			t.Errorf("clientGone(%v) = %v, want unchanged", err, got)
		}
	}
	if got := clientGone(ctx, domain.ErrInternal("x")); domain.FaultOf(got) != domain.FaultCaller {
		t.Errorf("internal error after the client left = %v, want client_canceled", got)
	}
	if got := clientGone(context.Background(), domain.ErrInternal("x")); domain.FaultOf(got) != domain.FaultNexus {
		t.Errorf("internal error with the client present = %v, want unchanged", got)
	}
}
