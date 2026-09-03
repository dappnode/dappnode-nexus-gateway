package openai

import (
	"errors"
	"net/http"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestParseResponse_MapsDeepSeekCacheHitTokens(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":17,"completion_tokens":9,"total_tokens":26,"prompt_cache_hit_tokens":7}}`)
	result, err := ParseResponse(data, domain.GenerateRequest{}, domain.PublicModel{})
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}
	if result.Usage == nil || result.Usage.CacheReadTokens != 7 {
		t.Fatalf("cache-read tokens = %+v, want 7", result.Usage)
	}
}

func TestParseResponse_RejectsNonzeroBaseResponse(t *testing.T) {
	_, err := ParseResponse([]byte(`{"base_resp":{"status_code":17,"status_msg":"provider error"}}`), domain.GenerateRequest{}, domain.PublicModel{})
	var gatewayErr *domain.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %v, want GatewayError", err)
	}
	if gatewayErr.HTTPStatus != http.StatusBadGateway || gatewayErr.Metadata["upstream_code"] != 17 {
		t.Fatalf("gateway error = %#v", gatewayErr)
	}
}

func TestParseResponse_AllowsAbsentOrZeroBaseResponse(t *testing.T) {
	for _, data := range []string{`{}`, `{"base_resp":{"status_code":0,"status_msg":""}}`} {
		if _, err := ParseResponse([]byte(data), domain.GenerateRequest{}, domain.PublicModel{}); err != nil {
			t.Fatalf("ParseResponse(%s): %v", data, err)
		}
	}
}
