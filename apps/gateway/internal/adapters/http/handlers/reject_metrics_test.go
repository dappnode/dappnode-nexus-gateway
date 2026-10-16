package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Requests the handler rejects before the generation service sees them are
// counted with the service's own failures.
func TestChatHandlerCountsRejectedRequests(t *testing.T) {
	logger := &privacyLogger{}
	handler := NewChatCompletionsHandler(nil, logger)
	for name, tc := range map[string]struct {
		auth, body, code string
	}{
		"missing key":  {body: `{"model":"m","messages":[]}`, code: domain.ErrCodeInvalidAPIKey},
		"invalid body": {auth: "Bearer sk-test", body: `{`, code: domain.ErrCodeInvalidField},
	} {
		t.Run(name, func(t *testing.T) {
			counter := metrics.ErrorsTotal.WithLabelValues(domain.EndpointChatCompletions, metrics.UnresolvedModel, "unknown",
				metrics.PhaseRequest, domain.FaultCaller, tc.code, "none")
			before := testutil.ToFloat64(counter)
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			handler.Handle(httptest.NewRecorder(), req)
			if got := testutil.ToFloat64(counter) - before; got != 1 {
				t.Fatalf("errors_total delta = %v, want 1", got)
			}
		})
	}
}

// Error lines carry the classification, and provider text that is not an
// identifier never reaches the log.
func TestErrorLogsCarryClassification(t *testing.T) {
	const secret = "PRIVATE PROMPT CANARY"
	logger := &privacyLogger{}
	err := domain.ErrInternal("an internal error occurred").WithMeta("dependency", "pii", "cause", domain.CausePIIFilterTimeout,
		"upstream_code", secret, "account_id", "acc_1")
	WriteErrorWithLog(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil), logger, err)
	out := logger.output.String()
	for _, want := range []string{"fault nexus", "cause pii_filter_timeout", "dependency pii", "account_id acc_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, secret) {
		t.Fatal("error log leaked provider text")
	}
}
