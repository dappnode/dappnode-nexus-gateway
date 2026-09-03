package ports

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// UsageMeter reserves request capacity and records completed usage through the
// internal Nexus metering service. The gateway reports facts; metering owns
// pricing, balance checks, and ledger mutations.
type UsageMeter interface {
	Reserve(ctx context.Context, auth domain.AuthContext, endpoint string, req domain.GenerateRequest, model domain.PublicModel, reservationRequestID string) (string, error)
	RecordSuccess(ctx context.Context, reservationID string, auth domain.AuthContext, endpoint string, req domain.GenerateRequest, resp domain.GenerateResult, model domain.PublicModel, latencyMs int64) error
	RecordFailure(ctx context.Context, reservationID *string, auth *domain.AuthContext, endpoint string, req *domain.GenerateRequest, model *domain.PublicModel, err error, partialUsage *domain.Usage, latencyMs int64) error
}
