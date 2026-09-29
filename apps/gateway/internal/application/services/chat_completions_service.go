package services

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// ChatCompletionsService handles /v1/chat/completions compatibility mapping.
type ChatCompletionsService struct {
	generate *GenerateService
	logger   ports.Logger
}

func NewChatCompletionsService(generate *GenerateService, logger ports.Logger) *ChatCompletionsService {
	return &ChatCompletionsService{generate: generate, logger: logger}
}

// Execute processes a non-streaming /v1/chat/completions request.
func (s *ChatCompletionsService) Execute(ctx context.Context, req domain.GenerateRequest, bearerToken string) (domain.GenerateResult, error) {
	result, _, err := s.generate.Execute(ctx, domain.EndpointChatCompletions, req, bearerToken)
	return result, err
}

// ExecuteStream processes a streaming /v1/chat/completions request.
func (s *ChatCompletionsService) ExecuteStream(ctx context.Context, req domain.GenerateRequest, bearerToken string) (ports.GenerationStream, *domain.PublicModel, error) {
	stream, _, model, err := s.generate.ExecuteStream(ctx, domain.EndpointChatCompletions, req, bearerToken)
	return stream, model, err
}

// Proxy runs a /v1/chat/completions request as a proxy; it returns
// ErrNotProxyable when the request needs the translating path.
func (s *ChatCompletionsService) Proxy(ctx context.Context, raw []byte, req domain.GenerateRequest, bearerToken string) (*ProxyCall, error) {
	return s.generate.Proxy(ctx, domain.EndpointChatCompletions, raw, req, bearerToken)
}
