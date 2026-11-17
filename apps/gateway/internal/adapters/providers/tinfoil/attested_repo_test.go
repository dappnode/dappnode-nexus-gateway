package tinfoil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Attested inference trusts the Tinfoil enclave for the code it runs, so that
// code is pinned in this binary, not taken from TINFOIL_CONFIG_REPO.

func TestAttestedTransportOnlyForThePinnedRepository(t *testing.T) {
	for _, tc := range []struct {
		repo, enclave, bundle string
		want                  bool
	}{
		{"", "inference.tinfoil.sh", "", true},
		{AttestedConfigRepo, "inference.tinfoil.sh", "", true},
		{"  " + AttestedConfigRepo + "  ", "inference.tinfoil.sh", "", true},
		{"nexus/leaky-router", "leaky.example", "", false},
		{"tinfoilsh/confidential-model-router-dev", "inference.tinfoil.sh", "", false},
		{"nexus/leaky-router", "", "https://bundle.example", false},
		// Without an enclave host or bundle URL the SDK ignores the repo and
		// verifies Tinfoil's default, so the request is safe to serve.
		{"nexus/leaky-router", "", "", true},
	} {
		t.Setenv("TINFOIL_CONFIG_REPO", tc.repo)
		t.Setenv("TINFOIL_ENCLAVE_HOST", tc.enclave)
		t.Setenv("TINFOIL_ATTESTATION_BUNDLE_URL", tc.bundle)
		if got := NewAdapter(time.Second).AttestedTransport(); got != tc.want {
			t.Errorf("repo=%q enclave=%q bundle=%q: AttestedTransport() = %v, want %v", tc.repo, tc.enclave, tc.bundle, got, tc.want)
		}
	}
}

// providerServer counts the requests that reach the provider.
func providerServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	sent := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("TINFOIL_CONFIG_REPO", "")
	t.Setenv("TINFOIL_API_KEY", "test-key")
	t.Setenv("TINFOIL_PROXY_BASE_URL", server.URL)
	return server, &sent
}

func TestAttestedInferenceRefusesAnEnclaveVerifiedAgainstAnotherRepository(t *testing.T) {
	server, sent := providerServer(t)
	client := fakeClient(server.Client())
	client.repo = "nexus/leaky-router"
	adapter := NewAdapterWithFactory(time.Second, &fakeFactory{client: client})
	ctx := domain.WithAttestedInference(context.Background())

	_, _, err := adapter.Complete(ctx, simpleRequest(), simpleModel(server.URL))
	assertUnpinnedRepositoryRefused(t, err)
	_, err = adapter.Stream(ctx, simpleRequest(), simpleModel(server.URL))
	assertUnpinnedRepositoryRefused(t, err)
	if *sent != 0 {
		t.Fatalf("the prompt reached the provider %d times", *sent)
	}
}

func TestAttestedInferenceSendsToThePinnedRepository(t *testing.T) {
	server, sent := providerServer(t)
	adapter := NewAdapterWithFactory(time.Second, &fakeFactory{client: fakeClient(server.Client())})

	_, proof, err := adapter.Complete(domain.WithAttestedInference(context.Background()), simpleRequest(), simpleModel(server.URL))
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if *sent != 1 || proof == nil {
		t.Fatalf("sent = %d, proof = %v; want one request with a proof", *sent, proof)
	}
}

// Requests that don't ask for attested inference keep today's behaviour,
// whatever repository the operator configured.
func TestOrdinaryRequestsIgnoreTheRepositoryPin(t *testing.T) {
	server, sent := providerServer(t)
	client := fakeClient(server.Client())
	client.repo = "tinfoilsh/another-router"

	if _, _, err := NewAdapterWithFactory(time.Second, &fakeFactory{client: client}).Complete(context.Background(), simpleRequest(), simpleModel(server.URL)); err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if *sent != 1 {
		t.Fatalf("sent = %d, want 1", *sent)
	}
}

func assertUnpinnedRepositoryRefused(t *testing.T, err error) {
	t.Helper()
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeProviderUnavailable {
		t.Fatalf("want %s, got %T %[2]v", domain.ErrCodeProviderUnavailable, err)
	}
	if domain.FaultOf(err) != domain.FaultNexus {
		t.Fatalf("fault = %q, want %q: a misconfigured repository is Nexus's fault", domain.FaultOf(err), domain.FaultNexus)
	}
}
