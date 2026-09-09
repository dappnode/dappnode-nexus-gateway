package handlers

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type privacyLogger struct{ output strings.Builder }

func (l *privacyLogger) Debug(m string, f ...any) { fmt.Fprint(&l.output, m, f) }
func (l *privacyLogger) Info(m string, f ...any)  { l.Debug(m, f...) }
func (l *privacyLogger) Warn(m string, f ...any)  { l.Debug(m, f...) }
func (l *privacyLogger) Error(m string, f ...any) { l.Debug(m, f...) }

func TestErrorLogsDoNotEchoRequestContents(t *testing.T) {
	const secret = "PRIVATE-PROMPT-CANARY"
	for _, err := range []error{errors.New(secret), domain.ErrInvalidField(secret), domain.ErrProviderError(502, secret).WithMeta("upstream_error", secret, "upstream_status", 400)} {
		logger := &privacyLogger{}
		WriteErrorWithLog(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil), logger, err)
		if strings.Contains(logger.output.String(), secret) {
			t.Fatal("error log leaked private content")
		}
	}
}
