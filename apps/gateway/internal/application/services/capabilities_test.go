package services

import (
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestStaleCapabilityFlagsDoNotRejectChatOptions(t *testing.T) {
	parallel := true
	model := domain.PublicModel{PublicModelID: "fixture", SupportsChatCompletions: true}
	req := domain.GenerateRequest{Stream: true, Tools: []domain.ToolDefinition{{}}, ParallelToolCalls: &parallel, StructuredOutput: true}
	if err := (&GenerateService{}).validateRequest(domain.EndpointChatCompletions, req, model); err != nil {
		t.Fatal(err)
	}
}

func TestSystemOneStillRejectsStreaming(t *testing.T) {
	model := domain.PublicModel{PublicModelID: "jev", ProviderConfig: domain.ProviderConfig{ProviderName: "typesafe"}}
	err := (&GenerateService{}).validateRequest(domain.EndpointSystemOne, domain.GenerateRequest{Stream: true}, model)
	if gwErr, ok := err.(*domain.GatewayError); !ok || gwErr.Code != domain.ErrCodeUnsupportedFeature {
		t.Fatalf("SystemOne stream = %v", err)
	}
}
