package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/dto"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
)

// Recovery catches panics and returns a 500 error.
func Recovery(logger ports.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"error", rec,
						"request_id", GetRequestID(r.Context()),
						"path", r.URL.Path,
					)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(dto.ErrorResponse{
						Error: dto.ErrorDetail{
							Type:    "internal_error",
							Code:    "internal_error",
							Message: "an internal error occurred",
						},
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
