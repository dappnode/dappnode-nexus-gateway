package ports

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// RouterClient resolves explicit router model IDs to concrete public models.
type RouterClient interface {
	Route(ctx context.Context, req domain.RouteRequest) (domain.RouteDecision, error)
}
