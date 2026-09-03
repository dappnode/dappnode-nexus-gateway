package domain_test

import (
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestGatewayError(t *testing.T) {
	err := domain.ErrInvalidAPIKey("test message")
	if err.HTTPStatus != 401 {
		t.Errorf("expected HTTP 401, got %d", err.HTTPStatus)
	}
	if err.Type != domain.ErrTypeAuthentication {
		t.Errorf("expected type %s, got %s", domain.ErrTypeAuthentication, err.Type)
	}
	if err.Code != domain.ErrCodeInvalidAPIKey {
		t.Errorf("expected code %s, got %s", domain.ErrCodeInvalidAPIKey, err.Code)
	}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestErrorConstructors(t *testing.T) {
	tests := []struct {
		name       string
		err        *domain.GatewayError
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{"InvalidAPIKey", domain.ErrInvalidAPIKey("bad"), 401, domain.ErrTypeAuthentication, domain.ErrCodeInvalidAPIKey},
		{"InactiveAPIKey", domain.ErrInactiveAPIKey(), 401, domain.ErrTypeAuthentication, domain.ErrCodeInactiveAPIKey},
		{"InactiveAccount", domain.ErrInactiveAccount(), 403, domain.ErrTypePermission, domain.ErrCodeInactiveAccount},
		{"UnsupportedModel", domain.ErrUnsupportedModel("foo"), 404, domain.ErrTypeInvalidRequest, domain.ErrCodeUnsupportedModel},
		{"UnsupportedEndpoint", domain.ErrUnsupportedEndpoint("m", "e"), 422, domain.ErrTypeInvalidRequest, domain.ErrCodeUnsupportedEndpoint},
		{"UnsupportedFeature", domain.ErrUnsupportedFeature("f"), 422, domain.ErrTypeInvalidRequest, domain.ErrCodeUnsupportedFeature},
		{"ProviderUnavailable", domain.ErrProviderUnavailable("p"), 503, domain.ErrTypeProvider, domain.ErrCodeProviderUnavailable},
		{"ProviderTimeout", domain.ErrProviderTimeout("p"), 504, domain.ErrTypeProvider, domain.ErrCodeProviderTimeout},
		{"InvalidField", domain.ErrInvalidField("f"), 400, domain.ErrTypeInvalidRequest, domain.ErrCodeInvalidField},
		{"InsufficientBalance", domain.ErrInsufficientBalance(), 402, domain.ErrTypePermission, domain.ErrCodeInsufficientBalance},
		{"UnknownField", domain.ErrUnknownField("f"), 400, domain.ErrTypeInvalidRequest, domain.ErrCodeUnknownField},
		{"Internal", domain.ErrInternal("i"), 500, domain.ErrTypeInternal, domain.ErrCodeInternalError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.HTTPStatus != tt.wantStatus {
				t.Errorf("HTTPStatus = %d, want %d", tt.err.HTTPStatus, tt.wantStatus)
			}
			if tt.err.Type != tt.wantType {
				t.Errorf("Type = %s, want %s", tt.err.Type, tt.wantType)
			}
			if tt.err.Code != tt.wantCode {
				t.Errorf("Code = %s, want %s", tt.err.Code, tt.wantCode)
			}
		})
	}
}

func TestAccountIsActive(t *testing.T) {
	a := domain.Account{Status: "active"}
	if !a.IsActive() {
		t.Error("expected active")
	}
	a.Status = "inactive"
	if a.IsActive() {
		t.Error("expected inactive")
	}
}

func TestPublicModelSupportsEndpoint(t *testing.T) {
	m := domain.PublicModel{
		SupportsChatCompletions: true,
	}
	if !m.SupportsEndpoint(domain.EndpointChatCompletions) {
		t.Error("should support chat_completions")
	}
	if m.SupportsEndpoint("unknown") {
		t.Error("should not support unknown")
	}
}

func TestPublicModelSupportsStreamForEndpoint(t *testing.T) {
	m := domain.PublicModel{
		SupportsChatCompletionsStream: true,
	}
	if !m.SupportsStreamForEndpoint(domain.EndpointChatCompletions) {
		t.Error("should support chat_completions stream")
	}
	m.SupportsChatCompletionsStream = false
	if m.SupportsStreamForEndpoint(domain.EndpointChatCompletions) {
		t.Error("should not support chat_completions stream when disabled")
	}
}
