package ports

import (
	"context"
	"io"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Provider forwards a client's OpenAI chat-completions request body upstream
// and returns the provider's response as it came. The adapter changes only
// what its PrepareProxyBody documents (the model name, usage for metering,
// and provider compatibility for valid OpenAI requests).
type Provider interface {
	// Stream sends a streaming request and returns the SSE body.
	Stream(ctx context.Context, raw []byte, model domain.PublicModel) (ProviderStream, error)
	// Complete sends a non-streaming request and returns the JSON body.
	Complete(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error)
}

// AttestedProvider is a provider adapter that sends a request only after it
// has verified the provider's hardware attestation, encrypted to a key bound
// to that attestation, so the provider's operator can't read the prompt
// either. It must fail closed: no verification, no request. Requests that
// require attested inference (domain.AttestedInferenceRequired) are sent only
// to providers whose adapter reports true.
type AttestedProvider interface {
	AttestedTransport() bool
}

// ImageProvider generates images through the OpenAI images API. A provider
// adapter that doesn't implement it can't serve image models.
type ImageProvider interface {
	// GenerateImages sends an images/generations request and returns the
	// provider's JSON, with the images inline.
	GenerateImages(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, error)
}

// ProviderStream is an upstream streaming response.
type ProviderStream struct {
	Body io.ReadCloser
	// Proof is set when the transport was verified before content was sent.
	Proof *domain.TinfoilTransportProof
}
