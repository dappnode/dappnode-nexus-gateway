package ports

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// AuthService authenticates API keys and returns the auth context.
type AuthService interface {
	AuthenticateAPIKey(ctx context.Context, apiKey string) (domain.AuthContext, error)
}
