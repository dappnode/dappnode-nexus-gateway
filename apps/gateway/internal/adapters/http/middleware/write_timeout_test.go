package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestActiveStreamOutlivesAbsoluteServerDeadline(t *testing.T) {
	handler := WriteTimeout(100 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			if _, err := fmt.Fprint(w, "data: token\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("active stream interrupted: %v %q", err, body)
	}
}
