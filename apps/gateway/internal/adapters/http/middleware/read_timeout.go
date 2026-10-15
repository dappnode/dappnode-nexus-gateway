package middleware

import (
	"io"
	"net/http"
	"time"
)

// ReadTimeout bounds each upload read, not the total upload or inference time.
// Header reading remains bounded separately by http.Server.ReadHeaderTimeout.
func ReadTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			controller := http.NewResponseController(w)
			_ = controller.SetReadDeadline(time.Time{})
			if r.Body != nil {
				r.Body = &readTimeoutBody{ReadCloser: r.Body, controller: controller, timeout: timeout}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type readTimeoutBody struct {
	io.ReadCloser
	controller *http.ResponseController
	timeout    time.Duration
}

func (b *readTimeoutBody) Read(buffer []byte) (int, error) {
	_ = b.controller.SetReadDeadline(time.Now().Add(b.timeout))
	n, err := b.ReadCloser.Read(buffer[:min(len(buffer), maxTransferChunk)])
	if err != nil {
		_ = b.controller.SetReadDeadline(time.Time{})
	}
	return n, err
}

func (b *readTimeoutBody) Close() error {
	_ = b.controller.SetReadDeadline(time.Now().Add(b.timeout))
	err := b.ReadCloser.Close()
	_ = b.controller.SetReadDeadline(time.Time{})
	return err
}
