package middleware

import (
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDAndBearerCORS(t *testing.T) {
	for _, id := range []string{"trace-123:span_1.test", strings.Repeat("a", 128), "", strings.Repeat("a", 129), "line\nbreak", "bad value", "é"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", id)
		req.Header.Set("Origin", "https://client.example")
		w := httptest.NewRecorder()
		var logged string
		RequestID(CORS(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { logged = GetRequestID(r.Context()) }))).ServeHTTP(w, req)
		got := w.Header().Get("X-Request-ID")
		if got != logged {
			t.Fatal("response and logging context disagree")
		}
		if id == "trace-123:span_1.test" || id == strings.Repeat("a", 128) {
			if got != id {
				t.Fatal("valid tracing identifier was replaced")
			}
		} else if _, err := uuid.Parse(got); err != nil || got == id {
			t.Fatal("invalid identifier was not replaced with a UUID")
		}
		if w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("credentialed CORS was enabled")
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "https://client.example" {
			t.Fatal("browser API clients lost CORS access")
		}
	}
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://client.example")
	w := httptest.NewRecorder()
	CORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("preflight reached handler") })).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal("bearer preflight failed")
	}
}
