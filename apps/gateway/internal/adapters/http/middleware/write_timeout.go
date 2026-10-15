package middleware

import (
	"io"
	"net/http"
	"time"
)

const maxTransferChunk = 32 << 10

// WriteTimeout bounds each network write, including encrypted response frames.
// It replaces an absolute server WriteTimeout, which would cut off a healthy
// long-running stream even while tokens and keepalives continue arriving.
func WriteTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Time{})
			next.ServeHTTP(&writeTimeoutWriter{ResponseWriter: w, controller: controller, timeout: timeout}, r)
		})
	}
}

type writeTimeoutWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

func (w *writeTimeoutWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *writeTimeoutWriter) Write(body []byte) (int, error) {
	if len(body) == 0 {
		_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
		return w.ResponseWriter.Write(body)
	}
	var written int
	for len(body) > 0 {
		chunk := body[:min(len(body), maxTransferChunk)]
		_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
		n, err := w.ResponseWriter.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
		if n != len(chunk) {
			return written, io.ErrShortWrite
		}
		body = body[n:]
	}
	return written, nil
}

func (w *writeTimeoutWriter) WriteHeader(status int) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	w.ResponseWriter.WriteHeader(status)
}

func (w *writeTimeoutWriter) FlushError() error {
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	return w.controller.Flush()
}

func (w *writeTimeoutWriter) Flush() { _ = w.FlushError() }
