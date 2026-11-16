package services

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// attestedStubProvider is a provider whose adapter verifies its upstream's
// attestation, like the Tinfoil adapter. withoutProof models an adapter that
// sends a request but returns no proof of the transport it used.
type attestedStubProvider struct {
	stubProvider
	withoutProof bool
}

func (*attestedStubProvider) AttestedTransport() bool { return true }

func (p *attestedStubProvider) proof() *domain.TinfoilTransportProof {
	if p.withoutProof {
		return nil
	}
	return &domain.TinfoilTransportProof{Provider: "attested", Status: domain.ProofStatusVerified}
}

func (p *attestedStubProvider) Complete(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error) {
	body, _, err := p.stubProvider.Complete(ctx, raw, model)
	if err != nil {
		return nil, nil, err
	}
	return body, p.proof(), nil
}

func (p *attestedStubProvider) Stream(ctx context.Context, raw []byte, model domain.PublicModel) (ports.ProviderStream, error) {
	stream, err := p.stubProvider.Stream(ctx, raw, model)
	if err != nil {
		return ports.ProviderStream{}, err
	}
	stream.Proof = p.proof()
	return stream, nil
}

// closeTrackingBody records whether the gateway closed an upstream stream.
type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

type attestedTestSetup struct {
	svc      *GenerateService
	meter    *stubUsageMeter
	router   *stubRouterClient
	pii      *fakePIIFilter
	attested *attestedStubProvider
	plain    *stubProvider
}

// newAttestedTestService serves "private/model" from an attested provider
// (falling back to a plain one), "open/model" from a plain provider, and the
// router "nexus/auto". The API key asks for PII masking.
func newAttestedTestService() *attestedTestSetup {
	setup := &attestedTestSetup{
		meter:    &stubUsageMeter{},
		router:   &stubRouterClient{decision: domain.RouteDecision{PublicModelID: "private/model"}},
		pii:      &fakePIIFilter{enabled: true},
		attested: &attestedStubProvider{},
		plain:    &stubProvider{},
	}
	private := domain.PublicModel{
		PublicModelID:           "private/model",
		ProviderModelID:         "attested-model",
		ProviderConfig:          domain.ProviderConfig{ProviderName: "attested"},
		Fallback:                &domain.ProviderTarget{ProviderModelID: "plain-model", ProviderConfig: domain.ProviderConfig{ProviderName: "plain"}},
		SupportsChatCompletions: true,
		MaxContextWindow:        1000,
		MaxOutputTokens:         100,
	}
	open := domain.PublicModel{
		PublicModelID:           "open/model",
		ProviderModelID:         "plain-model",
		ProviderConfig:          domain.ProviderConfig{ProviderName: "plain"},
		SupportsChatCompletions: true,
		MaxContextWindow:        1000,
		MaxOutputTokens:         100,
	}
	setup.svc = NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{
			Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive},
			APIKey:  domain.APIKey{ID: "key1", Active: true, PIIMode: domain.APIKeyPIIModeBalanced},
		}},
		&stubModelCatalog{
			models:  map[string]domain.PublicModel{"private/model": private, "open/model": open},
			routers: map[string]domain.RouterEntry{"nexus/auto": {RouterID: "nexus/auto", Active: true}},
		},
		setup.router,
		&stubProviderRegistry{providers: map[string]ports.Provider{
			"attested": setup.attested,
			"plain":    setup.plain,
		}},
		setup.meter,
		setup.pii,
		stubLogger{},
	)
	return setup
}

const attestedTestPrompt = `{"model":"private/model","messages":[{"role":"user","content":"Email Jane at jane@example.com"}]}`

func proxyAttested(t *testing.T, setup *attestedTestSetup, endpoint, model string, stream bool) (*ProxyCall, error) {
	t.Helper()
	ctx := domain.WithAttestedInference(context.Background())
	return setup.svc.Proxy(ctx, endpoint, []byte(attestedTestPrompt), domain.GenerateRequest{PublicModelID: model, Stream: stream}, "sk-test")
}

func requireAttestedInferenceUnavailable(t *testing.T, err error) {
	t.Helper()
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeAttestedInferenceUnavailable {
		t.Fatalf("error = %v, want %s", err, domain.ErrCodeAttestedInferenceUnavailable)
	}
	if gwErr.HTTPStatus != 422 || domain.FaultOf(err) != domain.FaultCaller {
		t.Fatalf("status %d fault %s, want 422 caller", gwErr.HTTPStatus, domain.FaultOf(err))
	}
}

func TestAttestedInference_SendsToAttestedProviderWithoutPIIAnalyzer(t *testing.T) {
	setup := newAttestedTestService()

	call, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "private/model", false)
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if setup.attested.calls != 1 || setup.plain.calls != 0 {
		t.Fatalf("provider calls = attested %d, plain %d; want 1, 0", setup.attested.calls, setup.plain.calls)
	}
	if setup.pii.calls != 0 {
		t.Fatalf("PII analyzer calls = %d, want 0: the analyzer is not attested code", setup.pii.calls)
	}
	if setup.attested.lastRaw != attestedTestPrompt {
		t.Fatalf("provider body = %s, want the prompt unchanged", setup.attested.lastRaw)
	}
	if !strings.Contains(string(call.Body), `"content":"ok"`) {
		t.Fatalf("response = %s", call.Body)
	}
	if setup.meter.successCalls != 1 {
		t.Fatalf("success calls = %d, want 1", setup.meter.successCalls)
	}
}

// recordingProofs records the Tinfoil proofs the gateway stores.
type recordingProofs struct{ stored int }

func (r *recordingProofs) UpsertTinfoilTransportProof(context.Context, domain.TinfoilTransportProof) error {
	r.stored++
	return nil
}

func (r *recordingProofs) GetTinfoilTransportProof(context.Context, string, string) (domain.TinfoilTransportProof, error) {
	return domain.TinfoilTransportProof{}, nil
}

func TestAttestedInference_StoresNoServerProof(t *testing.T) {
	for _, attested := range []bool{true, false} {
		setup := newAttestedTestService()
		proofs := &recordingProofs{}
		setup.svc.SetTinfoilProofRepository(proofs)
		catalog := setup.svc.catalog.(*stubModelCatalog)
		model := catalog.models["private/model"]
		model.ProofMode = domain.ProofModeTinfoilAttestedTransport
		model.ProviderConfig.ProviderName = "tinfoil" // Tinfoil proof mode requires the Tinfoil provider.
		catalog.models["private/model"] = model
		setup.svc.registry = &stubProviderRegistry{providers: map[string]ports.Provider{"tinfoil": setup.attested, "plain": setup.plain}}

		ctx := context.Background()
		if attested {
			ctx = domain.WithAttestedInference(ctx)
		}
		if _, err := setup.svc.Proxy(ctx, domain.EndpointChatCompletions, []byte(attestedTestPrompt), domain.GenerateRequest{PublicModelID: "private/model"}, "sk-test"); err != nil {
			t.Fatalf("Proxy returned error: %v", err)
		}
		if want := map[bool]int{true: 0, false: 1}[attested]; proofs.stored != want {
			t.Fatalf("attested=%v: stored proofs = %d, want %d", attested, proofs.stored, want)
		}
	}
}

func TestAttestedInference_WithoutTheRequirementMasksAsBefore(t *testing.T) {
	setup := newAttestedTestService()

	_, err := setup.svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(attestedTestPrompt),
		domain.GenerateRequest{PublicModelID: "open/model"}, "sk-test")
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if setup.pii.calls == 0 || setup.plain.calls != 1 {
		t.Fatalf("PII calls = %d, plain calls = %d; want masking and the plain provider", setup.pii.calls, setup.plain.calls)
	}
}

func TestAttestedInference_RefusesUnattestedProviderBeforeHoldingCredit(t *testing.T) {
	setup := newAttestedTestService()

	_, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "open/model", false)
	requireAttestedInferenceUnavailable(t, err)
	if setup.plain.calls != 0 || setup.attested.calls != 0 {
		t.Fatalf("provider calls = plain %d, attested %d; want none", setup.plain.calls, setup.attested.calls)
	}
	if setup.meter.reserveCalls != 0 {
		t.Fatalf("reserve calls = %d, want 0", setup.meter.reserveCalls)
	}
	if setup.pii.calls != 0 {
		t.Fatalf("PII analyzer calls = %d, want 0", setup.pii.calls)
	}
}

func TestAttestedInference_NeverCallsTheRouter(t *testing.T) {
	setup := newAttestedTestService()

	_, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "nexus/auto", false)
	requireAttestedInferenceUnavailable(t, err)
	if setup.router.calls != 0 {
		t.Fatalf("router calls = %d, want 0: the Router reads the prompt outside the enclave", setup.router.calls)
	}
	if setup.attested.calls != 0 || setup.meter.reserveCalls != 0 {
		t.Fatalf("provider calls = %d, reserve calls = %d; want none", setup.attested.calls, setup.meter.reserveCalls)
	}
}

func TestAttestedInference_UnknownModelStaysUnsupported(t *testing.T) {
	setup := newAttestedTestService()

	_, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "missing/model", false)
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeUnsupportedModel {
		t.Fatalf("error = %v, want unsupported_model", err)
	}
}

func TestAttestedInference_DoesNotFallBackToUnattestedProvider(t *testing.T) {
	setup := newAttestedTestService()
	primaryErr := domain.ErrProviderUnavailable("attested")
	setup.attested.err = primaryErr

	_, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "private/model", false)
	if err != primaryErr {
		t.Fatalf("error = %v, want the primary provider's error", err)
	}
	if setup.plain.calls != 0 {
		t.Fatalf("plain fallback calls = %d, want 0", setup.plain.calls)
	}
	if setup.meter.failureCalls != 1 || setup.meter.successCalls != 0 {
		t.Fatalf("metering = %d failures, %d successes; want the hold released once", setup.meter.failureCalls, setup.meter.successCalls)
	}
}

func TestAttestedInference_FallsBackToAnotherAttestedProvider(t *testing.T) {
	setup := newAttestedTestService()
	setup.attested.err = domain.ErrProviderUnavailable("attested")
	second := &attestedStubProvider{}
	setup.svc.registry = &stubProviderRegistry{providers: map[string]ports.Provider{
		"attested": setup.attested,
		"plain":    second, // The fallback's provider name now resolves to an attested adapter.
	}}

	if _, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "private/model", false); err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if setup.attested.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = primary %d, fallback %d; want 1 each", setup.attested.calls, second.calls)
	}
}

func TestAttestedInference_DiscardsAnswerWithoutTransportProof(t *testing.T) {
	setup := newAttestedTestService()
	setup.attested.withoutProof = true

	call, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "private/model", false)
	if err == nil || call != nil {
		t.Fatalf("Proxy = %v, %v; want an error and no answer", call, err)
	}
	if setup.plain.calls != 0 {
		t.Fatalf("plain fallback calls = %d, want 0", setup.plain.calls)
	}
	if setup.meter.successCalls != 0 || setup.meter.failureCalls != 1 {
		t.Fatalf("metering = %d successes, %d failures; want the hold released", setup.meter.successCalls, setup.meter.failureCalls)
	}
}

func TestAttestedInference_DiscardsStreamWithoutTransportProof(t *testing.T) {
	setup := newAttestedTestService()
	setup.attested.withoutProof = true
	body := &closeTrackingBody{Reader: strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"secret\"}}]}\n\n")}
	setup.svc.registry = &stubProviderRegistry{providers: map[string]ports.Provider{
		"attested": &streamBodyProvider{attestedStubProvider: setup.attested, body: body},
		"plain":    setup.plain,
	}}

	call, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "private/model", true)
	if err == nil || call != nil {
		t.Fatalf("Proxy = %v, %v; want an error and no stream", call, err)
	}
	if !body.closed {
		t.Fatal("upstream stream was left open")
	}
}

func TestAttestedInference_StreamsFromAttestedProvider(t *testing.T) {
	setup := newAttestedTestService()
	setup.attested.sse = "data: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"

	call, err := proxyAttested(t, setup, domain.EndpointChatCompletions, "private/model", true)
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if call.Stream == nil || call.Stream.proof == nil {
		t.Fatalf("stream = %+v, want a stream carrying its transport proof", call.Stream)
	}
	call.Stream.Close()
}

func TestAttestedInference_RefusesOtherEndpoints(t *testing.T) {
	for _, endpoint := range []string{domain.EndpointSystemOne, domain.EndpointImageGenerations} {
		setup := newAttestedTestService()
		_, err := proxyAttested(t, setup, endpoint, "private/model", false)
		requireAttestedInferenceUnavailable(t, err)
		if setup.attested.calls != 0 || setup.meter.reserveCalls != 0 {
			t.Fatalf("%s: provider calls = %d, reserve calls = %d; want none", endpoint, setup.attested.calls, setup.meter.reserveCalls)
		}
	}
}

// streamBodyProvider streams a fixed body, to observe whether it is closed.
type streamBodyProvider struct {
	*attestedStubProvider
	body io.ReadCloser
}

func (p *streamBodyProvider) Stream(context.Context, []byte, domain.PublicModel) (ports.ProviderStream, error) {
	p.calls++
	return ports.ProviderStream{Body: p.body, Proof: p.proof()}, nil
}
