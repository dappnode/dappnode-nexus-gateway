package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHeaderWaitDoesNotSetBodyLifetime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for i := 0; i < 8; i++ {
			fmt.Fprint(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(25 * time.Millisecond)
		}
	}))
	defer server.Close()
	request, _ := http.NewRequest("GET", server.URL, nil)
	response, err := DoWithHeaderTimeout(server.Client(), request, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "xxxxxxxx" {
		t.Fatalf("header timer canceled active body: %v %q", err, body)
	}
}

func TestSilentHeaderWaitReportsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	request, _ := http.NewRequest("GET", server.URL, nil)
	_, err := DoWithHeaderTimeout(server.Client(), request, 40*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("silent header wait = %v", err)
	}
}

func TestHeaderWaitPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	_, err := DoWithHeaderTimeout(http.DefaultClient, request, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation = %v", err)
	}
}
