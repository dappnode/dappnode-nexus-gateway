package middleware

import (
	"net/http"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
)

// Logging logs request details.
func Logging(logger ports.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(sw, r)

			durationMs := time.Since(start).Milliseconds()
			fields := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", durationMs,
				"request_id", GetRequestID(r.Context()),
			}
			if ua := r.Header.Get("User-Agent"); ua != "" {
				fields = append(fields, "user_agent", ua)
			}

			if sw.status >= 500 {
				logger.Error("request", fields...)
			} else if sw.status >= 400 {
				logger.Warn("request", fields...)
			} else {
				logger.Info("request", fields...)
			}
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
