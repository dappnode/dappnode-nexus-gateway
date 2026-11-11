package services

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// checkGuestModel refuses a guest key any request but chat on a model an
// operator ticked "Guests can use this model" (public_models.guest_allowed).
// It runs before routing, PII masking and Metering: a router can call a paid
// API (Jev calls TypeSafe) before credit is checked, and a free model would
// cost a guest nothing against its credit. Routers are never public models,
// so they are always refused.
func (s *GenerateService) checkGuestModel(ctx context.Context, endpoint, publicModelID string) error {
	if endpoint != domain.EndpointChatCompletions {
		return domain.ErrGuestModelNotAllowed(publicModelID)
	}
	model, err := s.catalog.GetPublicModel(ctx, publicModelID)
	if isUnsupportedModelError(err) {
		return domain.ErrGuestModelNotAllowed(publicModelID)
	}
	if err != nil {
		return err
	}
	if !model.GuestAllowed {
		return domain.ErrGuestModelNotAllowed(publicModelID)
	}
	return nil
}
