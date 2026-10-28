package services

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func upstream502() error {
	return domain.ErrProviderError(502, "provider server error").WithMeta("upstream_status", 502)
}

// scriptedProvider answers each call with the next error in its script, and
// succeeds once the script runs out.
type scriptedProvider struct {
	errs  []error
	calls int
}

func (p *scriptedProvider) next() error {
	p.calls++
	if len(p.errs) == 0 {
		return nil
	}
	err := p.errs[0]
	p.errs = p.errs[1:]
	return err
}

func (p *scriptedProvider) Complete(_ context.Context, _ []byte, _ domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error) {
	if err := p.next(); err != nil {
		return nil, nil, err
	}
	return []byte(stubAnswer), nil, nil
}

func (p *scriptedProvider) Stream(_ context.Context, _ []byte, _ domain.PublicModel) (ports.ProviderStream, error) {
	if err := p.next(); err != nil {
		return ports.ProviderStream{}, err
	}
	return ports.ProviderStream{Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, nil
}

func failing(n int, err func() error) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = err()
	}
	return out
}

func fastHealing() SelfHealing {
	cfg := DefaultSelfHealing
	cfg.RetryDelay = time.Millisecond
	return cfg
}

func TestBreakerOpensProbesAndCloses(t *testing.T) {
	now := time.Unix(1_000, 0)
	b := newBreaker(SelfHealing{BreakerFailures: 3, BreakerOpenFor: 30 * time.Second, BreakerMaxOpen: 50 * time.Second})
	b.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if tr := b.failure("p/m"); tr != nil {
			t.Fatalf("opened after %d failures", i+1)
		}
	}
	tr := b.failure("p/m")
	if tr == nil || tr.to != circuitOpen || tr.openFor != 30*time.Second {
		t.Fatalf("third failure: %+v, want open for 30s", tr)
	}
	if ok, wait := b.allow("p/m"); ok || wait != 30*time.Second {
		t.Fatalf("allow while open = %v, %v", ok, wait)
	}
	if ok, _ := b.allow("other/m"); !ok {
		t.Fatal("another target was blocked")
	}

	now = now.Add(31 * time.Second) // Half-open: one probe, the rest wait.
	if ok, _ := b.allow("p/m"); !ok {
		t.Fatal("no probe after the open period")
	}
	if ok, _ := b.allow("p/m"); ok {
		t.Fatal("a second request went through while the probe was out")
	}
	if tr := b.failure("p/m"); tr == nil || tr.openFor != 50*time.Second {
		t.Fatalf("failed probe: %+v, want reopen for 50s (doubled, capped)", tr)
	}

	now = now.Add(51 * time.Second)
	if ok, _ := b.allow("p/m"); !ok {
		t.Fatal("no second probe")
	}
	if tr := b.success("p/m"); tr == nil || tr.to != circuitClosed {
		t.Fatalf("successful probe: %+v, want closed", tr)
	}
	if ok, _ := b.allow("p/m"); !ok || b.state("p/m") != circuitClosed {
		t.Fatal("circuit not closed after a successful probe")
	}
}

func TestBreakerReleaseFreesTheProbe(t *testing.T) {
	now := time.Unix(1_000, 0)
	b := newBreaker(SelfHealing{BreakerFailures: 1, BreakerOpenFor: time.Second, BreakerMaxOpen: time.Second})
	b.now = func() time.Time { return now }
	b.failure("p/m")
	now = now.Add(2 * time.Second)
	b.allow("p/m")
	b.release("p/m") // The probe's client left: we learned nothing about the provider.
	if ok, _ := b.allow("p/m"); !ok {
		t.Fatal("probe slot not released")
	}
}

func TestBreakerOff(t *testing.T) {
	b := newBreaker(SelfHealing{})
	for i := 0; i < 10; i++ {
		b.failure("p/m")
	}
	if ok, _ := b.allow("p/m"); !ok {
		t.Fatal("a disabled breaker blocked a request")
	}
}

// A quick 502 with no fallback is retried once, and the caller gets the answer.
func TestProxy_RetriesAQuickFailureOnce(t *testing.T) {
	provider := &scriptedProvider{errs: []error{upstream502()}}
	svc := proxyService(&resultMeter{}, "", map[string]ports.Provider{"primary": provider}, false)
	svc.SetSelfHealing(fastHealing())
	retries := metrics.ProviderRetries.WithLabelValues("primary", "transient_retry")
	before := testutil.ToFloat64(retries)

	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key"); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
	if got := testutil.ToFloat64(retries) - before; got != 1 {
		t.Fatalf("retries delta = %v, want 1", got)
	}
}

// Requests the provider rejects, rate limits and canceled clients are not
// outages: no retry, and the circuit never opens.
func TestProxy_DoesNotRetryOrTripOnNonOutages(t *testing.T) {
	for name, errFn := range map[string]func() error{
		"rejected":     func() error { return domain.ErrProviderError(400, "bad").WithMeta("upstream_status", 400) },
		"rate limited": func() error { return domain.ErrProviderError(429, "slow down").WithMeta("upstream_status", 429) },
		"canceled":     func() error { return domain.ErrClientCanceled() },
	} {
		t.Run(name, func(t *testing.T) {
			provider := &scriptedProvider{errs: failing(20, errFn)}
			svc := proxyService(&resultMeter{}, "", map[string]ports.Provider{"primary": provider}, false)
			svc.SetSelfHealing(fastHealing())
			for i := 0; i < 10; i++ {
				svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key")
			}
			if provider.calls != 10 {
				t.Fatalf("provider calls = %d, want 10 (one per request)", provider.calls)
			}
		})
	}
}

// A provider that keeps failing gets its circuit opened: later requests fail
// at once with 503 and Retry-After, without reaching it.
func TestProxy_OpensTheCircuitOnAProviderThatKeepsFailing(t *testing.T) {
	provider := &scriptedProvider{errs: failing(100, upstream502)}
	svc := proxyService(&resultMeter{}, "", map[string]ports.Provider{"primary": provider}, false)
	svc.SetSelfHealing(fastHealing())
	run := func() error {
		_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key")
		return err
	}
	for i := 0; i < 3; i++ { // Two attempts each (with the retry): the fifth failure opens it.
		run()
	}
	calls := provider.calls
	err := run()
	gwErr, ok := err.(*domain.GatewayError)
	if !ok || gwErr.HTTPStatus != 503 || gwErr.Metadata["cause"] != domain.CauseCircuitOpen || gwErr.RetryAfter == "" {
		t.Fatalf("error = %#v, want 503 circuit_open with Retry-After", err)
	}
	if provider.calls != calls {
		t.Fatalf("provider called while its circuit was open (%d -> %d)", calls, provider.calls)
	}
	if v := testutil.ToFloat64(metrics.CircuitOpen.WithLabelValues("primary", "glm-5-3-flash")); v != 1 {
		t.Fatalf("circuit_open gauge = %v, want 1", v)
	}
}

// With a fallback, an open circuit sends requests straight to the fallback.
func TestProxy_SkipsAnOpenCircuitForTheFallback(t *testing.T) {
	primary := &scriptedProvider{errs: failing(100, upstream502)}
	fallback := &scriptedProvider{}
	svc := proxyService(&resultMeter{}, "", map[string]ports.Provider{"primary": primary, "fallback": fallback}, true)
	svc.SetSelfHealing(fastHealing())
	for i := 0; i < 8; i++ {
		if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key"); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	// One attempt per request until the fifth failure opens the circuit; then
	// the primary is skipped and every request goes to the fallback.
	if primary.calls != 5 || fallback.calls != 8 {
		t.Fatalf("calls = primary %d, fallback %d; want 5 and 8", primary.calls, fallback.calls)
	}
}
