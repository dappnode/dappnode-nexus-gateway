package handlers

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
)

type brokenBody struct{ io.Reader }

func (brokenBody) Close() error { return nil }

type failAfter struct {
	r   io.Reader
	err error
}

func (f *failAfter) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		return n, f.err
	}
	return n, err
}

func TestRelay_EndsEveryStream(t *testing.T) {
	finishedNoDone := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"
	for name, tc := range map[string]struct {
		body io.Reader
		want []string
		not  []string
	}{
		"relayed as sent":                {body: strings.NewReader(finishedNoDone + "data: [DONE]\n\n"), want: []string{`"content":"hi"`}},
		"finish without [DONE] gets one": {body: strings.NewReader(finishedNoDone), want: []string{"data: [DONE]"}},
		"broken mid-response says so": {
			body: &failAfter{r: strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}]}\n\n"), err: errors.New("connection reset")},
			want: []string{"stream_interrupted"}, not: []string{"[DONE]"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler := &ChatCompletionsHandler{logger: &confidentialTestLogger{}}
			var finished atomic.Bool
			handler.relay(recorder, httptest.NewRequest("POST", "/v1/chat/completions", nil), services.NewProxyStream(brokenBody{tc.body}, "m"), &finished)
			body := recorder.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("missing %q in %q", want, body)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(body, not) {
					t.Fatalf("unexpected %q in %q", not, body)
				}
			}
			if strings.Count(body, "data: [DONE]") > 1 {
				t.Fatalf("[DONE] twice: %q", body)
			}
		})
	}
}
