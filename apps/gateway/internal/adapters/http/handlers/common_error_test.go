package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/dto"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestErrorsExposeOriginAndStatusWithoutInternalMetadata(t *testing.T) {
	upstream := domain.ErrProviderError(429, "rate limited").WithMeta("upstream_status", 429, "upstream_error", "PRIVATE-DIAGNOSTIC-CANARY")
	upstream.RetryAfter = "17"
	for _, tc := range []struct {
		err    *domain.GatewayError
		origin string
		status int
		retry  string
	}{
		{upstream, "upstream", 429, "17"},
		{domain.ErrInvalidField("invalid input"), "gateway", 0, ""},
	} {
		recorder := httptest.NewRecorder()
		WriteError(recorder, tc.err)
		var got dto.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != tc.err.HTTPStatus || got.Error.Origin != tc.origin || got.Error.UpstreamStatus != tc.status || recorder.Header().Get("Retry-After") != tc.retry {
			t.Fatalf("error classification: %#v", got)
		}
		if strings.Contains(recorder.Body.String(), "PRIVATE-DIAGNOSTIC-CANARY") {
			t.Fatal("exposed internal diagnostic metadata")
		}
	}
}
