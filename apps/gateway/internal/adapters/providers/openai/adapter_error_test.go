package openai

import (
	"context"
	"net/http"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestAdapterMissingCredentialReturnsInternalError(t *testing.T) {
	adapter := NewAdapter(0)
	model := domain.PublicModel{ProviderConfig: domain.ProviderConfig{ProviderName: "novita"}}
	calls := []struct {
		name string
		call func() error
	}{
		{name: "complete", call: func() error {
			_, _, err := adapter.Complete(context.Background(), []byte(`{"messages":[]}`), model)
			return err
		}},
		{name: "stream", call: func() error {
			_, err := adapter.Stream(context.Background(), []byte(`{"messages":[]}`), model)
			return err
		}},
	}
	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			assertInternalCredentialError(t, tt.call(), "novita")
		})
	}
}

func assertInternalCredentialError(t *testing.T, err error, provider string) {
	t.Helper()
	gatewayErr, ok := err.(*domain.GatewayError)
	if !ok || gatewayErr.HTTPStatus != http.StatusInternalServerError || gatewayErr.Type != domain.ErrTypeInternal || gatewayErr.Code != domain.ErrCodeInternalError {
		t.Fatalf("error = %#v, want generic internal error", err)
	}
	if gatewayErr.Message != "an internal error occurred" || gatewayErr.Metadata["provider"] != provider {
		t.Fatalf("error = %#v, want generic response with provider log metadata", gatewayErr)
	}
}
