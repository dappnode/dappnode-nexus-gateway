package ports

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// ModelCatalog loads public models and routers from the metering runtime API.
type ModelCatalog interface {
	ListPublicModels(ctx context.Context) ([]domain.PublicModel, error)
	GetPublicModel(ctx context.Context, publicModelID string) (domain.PublicModel, error)
	ListRouters(ctx context.Context) ([]domain.RouterEntry, error)
	GetRouter(ctx context.Context, routerID string) (domain.RouterEntry, error)
}
