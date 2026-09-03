package tinfoil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/openai"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	tinfoilsdk "github.com/tinfoilsh/tinfoil-go"
	verifierclient "github.com/tinfoilsh/tinfoil-go/verifier/client"
)

const providerName = "tinfoil"

// VerifiedClient is the narrow SDK surface the adapter needs after attestation
// and encrypted transport setup have succeeded.
type VerifiedClient interface {
	HTTPClient() *http.Client
	Enclave() string
	Repo() string
	TransportMode() string
	GroundTruth() *verifierclient.GroundTruth
}

// VerifiedClientFactory creates a client that has already failed closed if
// attestation, key binding, or transport setup cannot be completed.
type VerifiedClientFactory interface {
	NewVerifiedClient(ctx context.Context, model domain.PublicModel) (VerifiedClient, error)
}

// Adapter is the Tinfoil provider adapter. It reuses the OpenAI-compatible
// request/response mapper, but all HTTP traffic goes through Tinfoil's
// attested EHBP client instead of the generic OpenAI adapter.
type Adapter struct {
	timeout time.Duration
	factory VerifiedClientFactory
	logger  ports.Logger
}

func NewAdapter(timeout time.Duration, logger ...ports.Logger) *Adapter {
	return NewAdapterWithFactory(timeout, SDKClientFactory{}, logger...)
}

func NewAdapterWithFactory(timeout time.Duration, factory VerifiedClientFactory, logger ...ports.Logger) *Adapter {
	var l ports.Logger
	if len(logger) > 0 {
		l = logger[0]
	}
	if factory == nil {
		factory = SDKClientFactory{}
	}
	return &Adapter{timeout: timeout, factory: factory, logger: l}
}

func (a *Adapter) Generate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	apiKey := os.Getenv(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return domain.GenerateResult{}, missingProviderCredentialError()
	}

	body := openai.BuildRequestBody(req, model)
	body["stream"] = false
	delete(body, "stream_options")

	verified, proof, err := a.newVerifiedClient(ctx, model)
	if err != nil {
		return domain.GenerateResult{}, err
	}

	respBody, err := a.do(ctx, verified, apiKey, body)
	if err != nil {
		return domain.GenerateResult{}, openai.MapProviderErrorWithCompatibilityContext(err, model, body)
	}

	result, err := openai.ParseResponse(respBody, req, model)
	if err != nil {
		return domain.GenerateResult{}, err
	}
	proof.ProviderResponseID = result.ID
	result.TinfoilProof = proof
	return result, nil
}

func (a *Adapter) StreamGenerate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (ports.GenerationStream, error) {
	apiKey := os.Getenv(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return nil, missingProviderCredentialError()
	}

	body := openai.BuildRequestBody(req, model)

	verified, proof, err := a.newVerifiedClient(ctx, model)
	if err != nil {
		return nil, err
	}

	resp, err := a.doStream(ctx, verified, apiKey, body)
	if err != nil {
		return nil, openai.MapProviderErrorWithCompatibilityContext(err, model, body)
	}

	return &Stream{
		inner: openai.NewStream(resp, providerName),
		proof: proof,
	}, nil
}

func missingProviderCredentialError() *domain.GatewayError {
	return domain.ErrInternal("an internal error occurred").WithMeta(
		"provider", providerName,
		"reason", "provider API key is not configured",
	)
}

func (a *Adapter) newVerifiedClient(ctx context.Context, model domain.PublicModel) (VerifiedClient, *domain.TinfoilTransportProof, error) {
	verified, err := a.factory.NewVerifiedClient(ctx, model)
	if err != nil {
		return nil, nil, domain.ErrProviderUnavailable(providerName).WithMeta("verification_error", err.Error())
	}
	if verified == nil || verified.HTTPClient() == nil || verified.GroundTruth() == nil {
		return nil, nil, domain.ErrProviderUnavailable(providerName).WithMeta("verification_error", "verified client returned no ground truth")
	}
	return verified, proofFromVerifiedClient(verified), nil
}

func (a *Adapter) do(ctx context.Context, verified VerifiedClient, apiKey string, body map[string]any) ([]byte, error) {
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	req, err := newProviderRequest(ctx, verified, apiKey, body)
	if err != nil {
		return nil, err
	}
	resp, err := verified.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("tinfoil request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Tinfoil response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, openai.ParseProviderError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

func (a *Adapter) doStream(ctx context.Context, verified VerifiedClient, apiKey string, body map[string]any) (*http.Response, error) {
	req, err := newProviderRequest(ctx, verified, apiKey, body)
	if err != nil {
		return nil, err
	}
	resp, err := verified.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("tinfoil stream request failed: %w", err)
	}
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		resp.Body.Close()
		return nil, openai.ParseProviderError(resp.StatusCode, respBody)
	}
	return resp, nil
}

func newProviderRequest(ctx context.Context, verified VerifiedClient, apiKey string, body map[string]any) (*http.Request, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL(verified), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return req, nil
}

func chatCompletionsURL(verified VerifiedClient) string {
	base := configuredProxyBaseURL()
	if base == "" {
		base = "https://" + verified.Enclave() + "/v1"
	}
	return base + "/chat/completions"
}

func configuredProxyBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("TINFOIL_PROXY_BASE_URL")), "/")
}

func proofFromVerifiedClient(verified VerifiedClient) *domain.TinfoilTransportProof {
	groundTruth := verified.GroundTruth()
	now := time.Now().UTC()
	evidence, _ := json.Marshal(map[string]any{
		"ground_truth":   groundTruth,
		"config_repo":    verified.Repo(),
		"transport_mode": verified.TransportMode(),
		"sdk_version":    tinfoilSDKVersion(),
		"verified_at":    now.Format(time.RFC3339Nano),
	})
	enclaveHost := stringPtr(groundTruth.EnclaveHost)
	if groundTruth.EnclaveHost == "" && verified.Enclave() != "" {
		enclaveHost = stringPtr(verified.Enclave())
	}
	return &domain.TinfoilTransportProof{
		Provider:                 providerName,
		EnclaveHost:              enclaveHost,
		ConfigRepo:               stringPtr(verified.Repo()),
		Digest:                   stringPtr(groundTruth.Digest),
		CodeFingerprint:          stringPtr(groundTruth.CodeFingerprint),
		EnclaveFingerprint:       stringPtr(groundTruth.EnclaveFingerprint),
		TLSPublicKey:             stringPtr(groundTruth.TLSPublicKey),
		HPKEPublicKey:            stringPtr(groundTruth.HPKEPublicKey),
		TransportMode:            stringPtr(verified.TransportMode()),
		SDKVersion:               stringPtr(tinfoilSDKVersion()),
		Status:                   domain.ProofStatusVerified,
		VerificationEvidenceJSON: json.RawMessage(evidence),
		CreatedAt:                now,
		VerifiedAt:               &now,
	}
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func tinfoilSDKVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/tinfoilsh/tinfoil-go" {
				if dep.Version != "" {
					return dep.Path + " " + dep.Version
				}
				return dep.Path
			}
		}
	}
	return "github.com/tinfoilsh/tinfoil-go"
}

// Stream decorates the OpenAI-compatible stream with Tinfoil proof evidence.
type Stream struct {
	inner ports.GenerationStream
	proof *domain.TinfoilTransportProof
}

func (s *Stream) Recv() (domain.StreamEvent, error) {
	return s.inner.Recv()
}

func (s *Stream) Close() error {
	return s.inner.Close()
}

func (s *Stream) VerifiedTransportProof() *domain.TinfoilTransportProof {
	if s.proof == nil {
		return nil
	}
	cp := *s.proof
	if len(s.proof.VerificationEvidenceJSON) > 0 {
		cp.VerificationEvidenceJSON = json.RawMessage(append([]byte(nil), s.proof.VerificationEvidenceJSON...))
	}
	return &cp
}

// SDKClientFactory is the production Tinfoil SDK factory.
type SDKClientFactory struct{}

func (SDKClientFactory) NewVerifiedClient(_ context.Context, model domain.PublicModel) (VerifiedClient, error) {
	opts := []tinfoilsdk.ClientOption{
		tinfoilsdk.WithTransport(tinfoilsdk.TransportEHBP),
	}
	if repo := strings.TrimSpace(os.Getenv("TINFOIL_CONFIG_REPO")); repo != "" {
		opts = append(opts, tinfoilsdk.WithRepo(repo))
	}
	if enclave := strings.TrimSpace(os.Getenv("TINFOIL_ENCLAVE_HOST")); enclave != "" {
		opts = append(opts, tinfoilsdk.WithEnclave(enclave))
	}
	// ProviderConfig.BaseURL is the OpenAI-compatible catalog URL. Do not treat
	// it as the EHBP request proxy by default; the SDK-selected verified enclave
	// is the native Tinfoil transport path. Operators can opt into an explicit
	// compatible EHBP proxy with TINFOIL_PROXY_BASE_URL.
	if proxyBaseURL := configuredProxyBaseURL(); proxyBaseURL != "" {
		opts = append(opts, tinfoilsdk.WithBaseURL(proxyBaseURL))
	}
	if bundleURL := strings.TrimSpace(os.Getenv("TINFOIL_ATTESTATION_BUNDLE_URL")); bundleURL != "" {
		opts = append(opts, tinfoilsdk.WithAttestationBundleURL(strings.TrimRight(bundleURL, "/")))
	}

	client, err := tinfoilsdk.NewClientWithOptions(opts...)
	if err != nil {
		return nil, err
	}
	groundTruth, err := client.Verify()
	if err != nil {
		return nil, err
	}
	return &sdkVerifiedClient{client: client, groundTruth: groundTruth}, nil
}

type sdkVerifiedClient struct {
	client      *tinfoilsdk.Client
	groundTruth *verifierclient.GroundTruth
}

func (c *sdkVerifiedClient) HTTPClient() *http.Client {
	return c.client.HTTPClient()
}

func (c *sdkVerifiedClient) Enclave() string {
	return c.client.Enclave()
}

func (c *sdkVerifiedClient) Repo() string {
	return c.client.Repo()
}

func (c *sdkVerifiedClient) TransportMode() string {
	return string(c.client.Transport())
}

func (c *sdkVerifiedClient) GroundTruth() *verifierclient.GroundTruth {
	return c.groundTruth
}
