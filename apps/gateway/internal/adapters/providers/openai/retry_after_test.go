package openai

import (
	"net/http"
	"testing"
)

func TestRetryGuidanceIsPreservedWithoutForwardingArbitraryHeaders(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"17", true}, {"Wed, 21 Oct 2015 07:28:00 GMT", true}, {"-1", false}, {"secret\r\nX-Secret: value", false}, {"provider-message", false}} {
		resp := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{tc.value}}}
		err := ParseProviderResponseError(resp, []byte(`{"error":{"message":"rate limit"}}`)).(*ProviderHTTPError)
		if (err.RetryAfter != "") != tc.valid {
			t.Fatalf("retry guidance %q: %q", tc.value, err.RetryAfter)
		}
	}
}
