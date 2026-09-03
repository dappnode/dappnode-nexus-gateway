package tinfoil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	verifierclient "github.com/tinfoilsh/tinfoil-go/verifier/client"
)

func TestGenerateUsesVerifiedClientAndStoresProofEvidence(t *testing.T) {
	t.Setenv("TINFOIL_API_KEY", "test-key")

	var seenRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenRequest = true
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected authorization header: %s", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "kimi-k2-6" || body["stream"] != false {
			t.Fatalf("unexpected request body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"cmpl-tinfoil-1",
			"created":1710000000,
			"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
		}`)
	}))
	defer server.Close()
	t.Setenv("TINFOIL_PROXY_BASE_URL", server.URL)

	factory := &fakeFactory{client: fakeClient(server.Client())}
	result, err := NewAdapterWithFactory(time.Second, factory).Generate(context.Background(), simpleRequest(false), simpleModel(server.URL))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if !seenRequest {
		t.Fatalf("provider request was not sent")
	}
	if result.ID != "cmpl-tinfoil-1" || result.TinfoilProof == nil {
		t.Fatalf("unexpected result/proof: id=%q proof=%#v", result.ID, result.TinfoilProof)
	}
	if result.TinfoilProof.ProviderResponseID != "cmpl-tinfoil-1" {
		t.Fatalf("proof response id was not filled: %#v", result.TinfoilProof.ProviderResponseID)
	}
	if result.TinfoilProof.EnclaveHost == nil || *result.TinfoilProof.EnclaveHost != "inference.tinfoil.sh" {
		t.Fatalf("unexpected enclave host in proof: %#v", result.TinfoilProof.EnclaveHost)
	}
	if result.TinfoilProof.TransportMode == nil || *result.TinfoilProof.TransportMode != "ehbp" {
		t.Fatalf("unexpected transport mode in proof: %#v", result.TinfoilProof.TransportMode)
	}
	if len(result.TinfoilProof.VerificationEvidenceJSON) == 0 {
		t.Fatalf("expected verification evidence JSON")
	}
}

func TestStreamGenerateReturnsVerifiedTransportProof(t *testing.T) {
	t.Setenv("TINFOIL_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"cmpl-stream-1\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"cmpl-stream-1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("TINFOIL_PROXY_BASE_URL", server.URL)

	stream, err := NewAdapterWithFactory(time.Second, &fakeFactory{client: fakeClient(server.Client())}).StreamGenerate(
		context.Background(),
		simpleRequest(true),
		simpleModel(server.URL),
	)
	if err != nil {
		t.Fatalf("StreamGenerate returned error: %v", err)
	}
	defer stream.Close()

	var text string
	var completed bool
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv returned error: %v", recvErr)
		}
		if event.ContentDelta != nil {
			text += *event.ContentDelta
		}
		if event.Type == domain.StreamEventCompleted {
			completed = true
		}
	}
	if text != "hi" || !completed {
		t.Fatalf("unexpected stream events: text=%q completed=%v", text, completed)
	}
	proofProvider, ok := stream.(interface {
		VerifiedTransportProof() *domain.TinfoilTransportProof
	})
	if !ok {
		t.Fatalf("stream does not expose proof evidence")
	}
	proof := proofProvider.VerifiedTransportProof()
	if proof == nil || proof.TransportMode == nil || *proof.TransportMode != "ehbp" {
		t.Fatalf("unexpected stream proof: %#v", proof)
	}
}

func TestGenerateFailsClosedWhenAttestationFails(t *testing.T) {
	t.Setenv("TINFOIL_API_KEY", "test-key")

	var called bool
	factory := &fakeFactory{err: errors.New("attestation failed"), onCall: func() { called = true }}
	_, err := NewAdapterWithFactory(time.Second, factory).Generate(context.Background(), simpleRequest(false), simpleModel("https://example.invalid/v1"))
	if err == nil {
		t.Fatalf("expected error")
	}
	if !called {
		t.Fatalf("expected factory to be called")
	}
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.Code != domain.ErrCodeProviderUnavailable {
		t.Fatalf("expected provider unavailable GatewayError, got %T %[1]v", err)
	}
}

func TestGenerateDoesNotVerifyWithoutAPIKey(t *testing.T) {
	t.Setenv("TINFOIL_API_KEY", "")

	var called bool
	factory := &fakeFactory{client: fakeClient(http.DefaultClient), onCall: func() { called = true }}
	_, err := NewAdapterWithFactory(time.Second, factory).Generate(context.Background(), simpleRequest(false), simpleModel("https://example.invalid/v1"))
	if err == nil {
		t.Fatalf("expected error")
	}
	if called {
		t.Fatalf("factory should not be called without an API key")
	}
}

func TestGenerateMapsProviderErrors(t *testing.T) {
	t.Setenv("TINFOIL_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer server.Close()
	t.Setenv("TINFOIL_PROXY_BASE_URL", server.URL)

	_, err := NewAdapterWithFactory(time.Second, &fakeFactory{client: fakeClient(server.Client())}).Generate(
		context.Background(),
		simpleRequest(false),
		simpleModel(server.URL),
	)
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) || gwErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("expected mapped 429 GatewayError, got %T %[1]v", err)
	}
}

func TestRequestURLDefaultsToVerifiedEnclave(t *testing.T) {
	t.Setenv("TINFOIL_PROXY_BASE_URL", "")

	req, err := newProviderRequest(context.Background(), fakeClient(http.DefaultClient), "test-key", map[string]any{"model": "deepseek-v4-pro"})
	if err != nil {
		t.Fatalf("newProviderRequest returned error: %v", err)
	}
	if got, want := req.URL.String(), "https://inference.tinfoil.sh/v1/chat/completions"; got != want {
		t.Fatalf("request URL = %q, want %q", got, want)
	}
}

func TestRequestURLUsesExplicitProxyBaseURL(t *testing.T) {
	t.Setenv("TINFOIL_PROXY_BASE_URL", "https://proxy.example.test/v1/")

	req, err := newProviderRequest(context.Background(), fakeClient(http.DefaultClient), "test-key", map[string]any{"model": "deepseek-v4-pro"})
	if err != nil {
		t.Fatalf("newProviderRequest returned error: %v", err)
	}
	if got, want := req.URL.String(), "https://proxy.example.test/v1/chat/completions"; got != want {
		t.Fatalf("request URL = %q, want %q", got, want)
	}
}

func simpleRequest(stream bool) domain.GenerateRequest {
	role := "user"
	content := "hello"
	return domain.GenerateRequest{
		PublicModelID: "tinfoil/kimi-k2-6",
		Stream:        stream,
		Input: []domain.InputItem{{
			Type:    domain.InputItemTypeMessage,
			Role:    &role,
			Content: &content,
		}},
	}
}

func simpleModel(baseURL string) domain.PublicModel {
	return domain.PublicModel{
		PublicModelID:     "tinfoil/kimi-k2-6",
		ProviderModelID:   "tinfoil-kimi-k2-6",
		UpstreamModelName: "kimi-k2-6",
		ProofMode:         domain.ProofModeTinfoilAttestedTransport,
		MaxOutputTokens:   8192,
		ProviderConfig: domain.ProviderConfig{
			ProviderName:    "tinfoil",
			BaseURL:         baseURL,
			APIKeySecretRef: "TINFOIL_API_KEY",
		},
	}
}

type fakeFactory struct {
	client VerifiedClient
	err    error
	onCall func()
}

func (f *fakeFactory) NewVerifiedClient(context.Context, domain.PublicModel) (VerifiedClient, error) {
	if f.onCall != nil {
		f.onCall()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

type fakeVerifiedClient struct {
	httpClient  *http.Client
	groundTruth *verifierclient.GroundTruth
}

func fakeClient(httpClient *http.Client) *fakeVerifiedClient {
	return &fakeVerifiedClient{
		httpClient: httpClient,
		groundTruth: &verifierclient.GroundTruth{
			EnclaveHost:        "inference.tinfoil.sh",
			TLSPublicKey:       "tls-key",
			HPKEPublicKey:      "hpke-key",
			Digest:             "sha256:abc",
			CodeFingerprint:    "code-fp",
			EnclaveFingerprint: "enclave-fp",
		},
	}
}

func (c *fakeVerifiedClient) HTTPClient() *http.Client {
	return c.httpClient
}

func (c *fakeVerifiedClient) Enclave() string {
	return "inference.tinfoil.sh"
}

func (c *fakeVerifiedClient) Repo() string {
	return "tinfoilsh/confidential-model-router"
}

func (c *fakeVerifiedClient) TransportMode() string {
	return "ehbp"
}

func (c *fakeVerifiedClient) GroundTruth() *verifierclient.GroundTruth {
	return c.groundTruth
}
