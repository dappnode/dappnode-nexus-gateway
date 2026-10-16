package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
)

// Metrics records HTTP-level Prometheus metrics for every request. It is
// intentionally placed as the outermost middleware so the recorded duration
// covers the full handler chain.
//
// The route label comes from route, which names the mux pattern a request
// matches: inner middleware passes the mux a copy of the request, so
// r.Pattern is never set on the request seen here. Requests that match no
// route use the fixed "unmatched" label so a flood of 404s cannot create
// unbounded series.
func Metrics(route func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return metricsHandler(next, route)
	}
}

func metricsHandler(next http.Handler, routeOf func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw := &metricsWriter{ResponseWriter: w, status: http.StatusOK}

		metrics.HTTPInflight.Inc()
		start := time.Now()
		next.ServeHTTP(mw, r)
		duration := time.Since(start).Seconds()
		metrics.HTTPInflight.Dec()

		route := routeOf(r)
		if route == "" {
			route = "unmatched"
		}
		statusClass := statusClass(mw.status)
		metrics.HTTPRequestsTotal.WithLabelValues(route, r.Method, statusClass).Inc()
		metrics.HTTPRequestDuration.WithLabelValues(route, r.Method).Observe(duration)
	})
}

// metricsWriter captures the response status code. It preserves only the
// first status code written, since HTTP honors the first WriteHeader; later
// calls (e.g. a recovery handler attempting a 500 after output started) must
// not override what the client actually received. Flusher is forwarded so
// streaming endpoints keep working.
type metricsWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *metricsWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func statusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500:
		return "5xx"
	default:
		return strconv.Itoa(code)
	}
}
