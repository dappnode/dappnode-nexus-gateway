package ports

import (
	"context"

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
