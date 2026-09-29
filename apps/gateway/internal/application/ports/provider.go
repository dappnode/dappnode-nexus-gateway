package ports

import (
	"context"
	"io"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// GenerationProvider translates canonical requests to upstream provider calls.
type GenerationProvider interface {
	Generate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error)
	StreamGenerate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (GenerationStream, error)
}

// GenerationStream reads canonical stream events from a provider.
type GenerationStream interface {
	Recv() (domain.StreamEvent, error)
	Close() error
}

// VerifiedTransportProofProvider exposes safe proof evidence produced by a
// provider adapter whose request transport is verified before user content is
// sent upstream.
type VerifiedTransportProofProvider interface {
	VerifiedTransportProof() *domain.TinfoilTransportProof
}

// ProxyProvider forwards the client's OpenAI chat-completions request body
// upstream (with the model and the few documented edits the adapter makes)
// and returns the provider's response as it came. Providers that speak the OpenAI
// wire format implement it; the gateway then acts as a proxy instead of
// translating through canonical events.
type ProxyProvider interface {
	// ProxyStream sends a streaming request and returns the SSE body.
	ProxyStream(ctx context.Context, raw []byte, model domain.PublicModel) (ProxyResponse, error)
	// ProxyJSON sends a non-streaming request and returns the JSON body.
	ProxyJSON(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error)
}

// ProxyResponse is an upstream streaming response.
type ProxyResponse struct {
	Body io.ReadCloser
	// Proof is set when the transport was verified before content was sent.
	Proof *domain.TinfoilTransportProof
}
