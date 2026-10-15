package openai

import (
	"context"
	"errors"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestUpstreamErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		upstream, downstream int
		code                 string
	}{
		{400, 400, domain.ErrCodeProviderRejectedRequest},
		{404, 404, domain.ErrCodeProviderRejectedRequest},
		{422, 422, domain.ErrCodeProviderRejectedRequest},
		{401, 502, domain.ErrCodeProviderConfiguration},
		{402, 502, domain.ErrCodeProviderConfiguration},
		{403, 502, domain.ErrCodeProviderConfiguration},
		{429, 429, domain.ErrCodeProviderRateLimited},
		{500, 500, domain.ErrCodeProviderError},
		{502, 502, domain.ErrCodeProviderError},
		{503, 503, domain.ErrCodeProviderUnavailable},
		{504, 504, domain.ErrCodeProviderTimeout},
	} {
		err := mapProviderError(&ProviderHTTPError{StatusCode: tc.upstream, Body: "rejected"}, "provider")
		var got *domain.GatewayError
		if !errors.As(err, &got) || got.HTTPStatus != tc.downstream || got.Code != tc.code || got.Metadata["upstream_status"] != tc.upstream {
			t.Fatalf("upstream %d: %#v", tc.upstream, got)
		}
	}
	if got := mapProviderError(context.Canceled, "provider").(*domain.GatewayError); got.Code != domain.ErrCodeClientCanceled {
		t.Fatal(got)
	}
}
