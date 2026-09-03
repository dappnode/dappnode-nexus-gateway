package handlers

import (
	"encoding/json"
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
	WriteJSON(w, gwErr.HTTPStatus, dto.ErrorResponse{
		Error: dto.ErrorDetail{
			Type:    gwErr.Type,
			Code:    gwErr.Code,
			Message: gwErr.Message,
		},
	})
}

// WriteErrorWithLog writes a GatewayError as a JSON error response and logs it server-side.
func WriteErrorWithLog(w http.ResponseWriter, r *http.Request, logger ports.Logger, err error) {
	gwErr, ok := err.(*domain.GatewayError)
	if !ok {
		logger.Error("untyped internal error",
			"request_id", middleware.GetRequestID(r.Context()),
			"path", r.URL.Path,
			"original_error", err.Error(),
		)
		gwErr = domain.ErrInternal("an internal error occurred")
	}

	// Log at warn for client errors, error for server errors.
	if gwErr.HTTPStatus >= 500 {
		fields := []any{
			"request_id", middleware.GetRequestID(r.Context()),
			"status", gwErr.HTTPStatus,
			"gateway_status", gwErr.HTTPStatus,
			"error_code", gwErr.Code,
			"error", gwErr.Message,
		}
		fields = append(fields, gwErr.LogFields()...)
		logger.Error("request failed", fields...)
	} else if gwErr.HTTPStatus >= 400 {
		logger.Warn("request error",
			"request_id", middleware.GetRequestID(r.Context()),
			"status", gwErr.HTTPStatus,
			"gateway_status", gwErr.HTTPStatus,
			"error_code", gwErr.Code,
			"error", gwErr.Message,
		)
	}

	WriteJSON(w, gwErr.HTTPStatus, dto.ErrorResponse{
		Error: dto.ErrorDetail{
			Type:    gwErr.Type,
			Code:    gwErr.Code,
			Message: gwErr.Message,
		},
	})
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
