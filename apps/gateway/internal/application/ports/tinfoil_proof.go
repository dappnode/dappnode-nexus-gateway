package ports

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// TinfoilTransportProofRepository persists and retrieves safe per-response
// Tinfoil attested-transport proof evidence.
type TinfoilTransportProofRepository interface {
	UpsertTinfoilTransportProof(ctx context.Context, proof domain.TinfoilTransportProof) error
	GetTinfoilTransportProof(ctx context.Context, accountID, providerResponseID string) (domain.TinfoilTransportProof, error)
}
