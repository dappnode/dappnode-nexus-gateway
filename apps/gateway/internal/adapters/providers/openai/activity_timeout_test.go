package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNonStreamingKeepalivesOutliveSilenceTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for i := 0; i < 8; i++ {
			fmt.Fprint(w, "\n")
			w.(http.Flusher).Flush()
			time.Sleep(25 * time.Millisecond)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"complete"}}]}`)
	}))
	defer server.Close()
	body, err := NewClient(150*time.Millisecond).Do(context.Background(), server.URL, "fixture", map[string]any{"model": "test"})
	if err != nil || len(body) == 0 {
		t.Fatalf("active JSON response ended early: %v", err)
	}
}
