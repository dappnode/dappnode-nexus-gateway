package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/dto"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

const maxRequestBodySize = 10 * 1024 * 1024 // 10 MB

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes a GatewayError as a JSON error response.
func WriteError(w http.ResponseWriter, err error) {
	gwErr, ok := err.(*domain.GatewayError)
	if !ok {
		gwErr = domain.ErrInternal("an internal error occurred")
	}
	if gwErr.RetryAfter != "" {
		w.Header().Set("Retry-After", gwErr.RetryAfter)
	}
	WriteJSON(w, gwErr.HTTPStatus, dto.ErrorResponse{
		Error: errorDetail(gwErr),
	})
}

// WriteErrorWithLog writes a GatewayError as a JSON error response and logs it server-side.
func WriteErrorWithLog(w http.ResponseWriter, r *http.Request, logger ports.Logger, err error) {
	gwErr, ok := err.(*domain.GatewayError)
	if !ok {
		logger.Error("untyped internal error",
			"request_id", middleware.GetRequestID(r.Context()),
			"path", r.URL.Path,
			"error_type", fmt.Sprintf("%T", err),
		)
		gwErr = domain.ErrInternal("an internal error occurred")
	}

	// Log at warn for client errors, error for server errors. Provider errors
	// can echo prompts: only the classification and log-safe metadata go in.
	fields := []any{
		"request_id", middleware.GetRequestID(r.Context()),
		"path", r.URL.Path,
		"status", gwErr.HTTPStatus,
	}
	fields = append(fields, domain.DiagnosticFields(gwErr)...)
	if gwErr.HTTPStatus >= 500 {
		logger.Error("request failed", fields...)
	} else if gwErr.HTTPStatus >= 400 {
		logger.Warn("request error", fields...)
	}

	if gwErr.RetryAfter != "" {
		w.Header().Set("Retry-After", gwErr.RetryAfter)
	}
	WriteJSON(w, gwErr.HTTPStatus, dto.ErrorResponse{
		Error: errorDetail(gwErr),
	})
}

// Diagnostic extensions expose only classification and numeric status.
// Arbitrary internal metadata is never serialized to clients.
func errorDetail(err *domain.GatewayError) dto.ErrorDetail {
	detail := dto.ErrorDetail{Type: err.Type, Code: err.Code, Message: err.Message, Origin: "gateway"}
	if err.Type == domain.ErrTypeProvider {
		detail.Origin = "upstream"
	}
	if status, ok := err.Metadata["upstream_status"].(int); ok {
		detail.UpstreamStatus = status
	}
	return detail
}

// ExtractBearerToken extracts the bearer token from the Authorization header.
func ExtractBearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", domain.ErrInvalidAPIKey("missing Authorization header")
	}
	if len(auth) < 8 || auth[:7] != "Bearer " {
		return "", domain.ErrInvalidAPIKey("malformed Authorization header, expected 'Bearer <token>'")
	}
	token := auth[7:]
	if token == "" {
		return "", domain.ErrInvalidAPIKey("empty bearer token")
	}
	return token, nil
}
