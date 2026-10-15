package services

import (
	"context"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// keeperMeter is a meter whose credit hold can be lost before inference ends.
type keeperMeter struct {
	stubUsageMeter
	lose    bool
	stopped bool
}

func (m *keeperMeter) KeepReservation(_ context.Context, _ string, onLost func()) func() {
	if m.lose {
		onLost()
	}
	return func() { m.stopped = true }
}

// waitingProvider answers only when its request is canceled, like a slow
// upstream that is still working.
type waitingProvider struct{ stubProvider }

func (p *waitingProvider) Complete(ctx context.Context, _ []byte, _ domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func keeperService(meter *keeperMeter, provider ports.Provider) *GenerateService {
	return NewGenerateService(
		&stubAuthService{authCtx: domain.AuthContext{Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive}, APIKey: domain.APIKey{ID: "key1", Active: true}}},
		&stubModelCatalog{model: domain.PublicModel{PublicModelID: "model", ProviderModelID: "provider-model", ProviderConfig: domain.ProviderConfig{ProviderName: "openai"}, SupportsChatCompletions: true, MaxContextWindow: 1000, MaxOutputTokens: 100}},
		nil, &stubProviderRegistry{providers: map[string]ports.Provider{"openai": provider}}, meter, nil, stubLogger{},
	)
}

func TestProxy_StopsInferenceWhenCreditHoldIsLost(t *testing.T) {
	meter := &keeperMeter{lose: true}
	_, err := keeperService(meter, &waitingProvider{}).Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "model"}, "sk-test")
	gwErr, ok := err.(*domain.GatewayError)
	if !ok || gwErr.Code != domain.ErrCodeInternalError || !strings.Contains(gwErr.Message, "credit reservation") {
		t.Fatalf("lost hold reported as %v, want an internal error, not a user cancellation", err)
	}
	if meter.failureCalls != 1 || !meter.stopped {
		t.Fatalf("failure recorded %d times, keeper stopped %v", meter.failureCalls, meter.stopped)
	}
}

func TestProxy_StopsKeepingHoldAfterCompletion(t *testing.T) {
	meter := &keeperMeter{}
	if _, err := keeperService(meter, &stubProvider{}).Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{}`), domain.GenerateRequest{PublicModelID: "model"}, "sk-test"); err != nil {
		t.Fatal(err)
	}
	if meter.successCalls != 1 || !meter.stopped {
		t.Fatalf("success recorded %d times, keeper stopped %v", meter.successCalls, meter.stopped)
	}
}
