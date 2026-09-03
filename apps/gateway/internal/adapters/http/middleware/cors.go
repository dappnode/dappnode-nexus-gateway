package middleware

import "net/http"

// CORS adds permissive CORS headers so browser SPAs can call the gateway
// directly — both the public landing page hitting the unauthenticated
// GET /v1/models endpoint and the user-ui chat playground hitting
// /v1/chat/completions with a Bearer API key. Origin is reflected only when
// present, with `Vary: Origin` to keep intermediate caches correct.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Ehbp-Encapsulated-Key, X-Request-ID")
		w.Header().Set("Access-Control-Expose-Headers", "Ehbp-Response-Nonce, X-Request-ID")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
