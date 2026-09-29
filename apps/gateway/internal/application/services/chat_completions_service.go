package services

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// ChatCompletionsService handles /v1/chat/completions.
type ChatCompletionsService struct {
	generate *GenerateService
	logger   ports.Logger
}

func NewChatCompletionsService(generate *GenerateService, logger ports.Logger) *ChatCompletionsService {
	return &ChatCompletionsService{generate: generate, logger: logger}
}

// Proxy forwards a request; raw is the client's body, req what the gateway
// read from it.
func (s *ChatCompletionsService) Proxy(ctx context.Context, raw []byte, req domain.GenerateRequest, bearerToken string) (*ProxyCall, error) {
	return s.generate.Proxy(ctx, domain.EndpointChatCompletions, raw, req, bearerToken)
}
