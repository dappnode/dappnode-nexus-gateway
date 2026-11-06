package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The web chat sends an Idempotency-Key with image requests; a browser only
// sends it when the preflight allows it.
func TestCORSAllowsIdempotencyKey(t *testing.T) {
	handler := CORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/v1/images/generations", nil)
	req.Header.Set("Origin", "https://nexus.dappnode.com")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type, idempotency-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") {
		t.Fatalf("Allow-Headers = %q, want Idempotency-Key", rec.Header().Get("Access-Control-Allow-Headers"))
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "Idempotent-Replayed") {
		t.Fatal("the replay header must be readable by the browser")
	}
}
