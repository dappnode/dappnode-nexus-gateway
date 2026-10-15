package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActiveUploadAndModelWaitOutliveAbsoluteDeadlines(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		// Waiting for model work must not inherit the final upload deadline.
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, string(body))
	})
	server := httptest.NewUnstartedServer(ReadTimeout(150 * time.Millisecond)(WriteTimeout(150 * time.Millisecond)(handler)))
	server.Config.ReadTimeout = 50 * time.Millisecond
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		for i := 0; i < 8; i++ {
			if _, err := writer.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	response, err := server.Client().Post(server.URL, "text/plain", reader)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(body) != "xxxxxxxx" {
		t.Fatalf("active upload/model wait interrupted: %d %q %v", response.StatusCode, body, err)
	}
}
