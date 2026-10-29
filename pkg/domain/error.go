package domain

import (
	"fmt"
)

// Error types as defined in the spec.
const (
	ErrTypeInvalidRequest = "invalid_request_error"
	ErrTypeAuthentication = "authentication_error"
	ErrTypePermission     = "permission_error"
	ErrTypeProvider       = "provider_error"
	ErrTypeInternal       = "internal_error"
	ErrTypeRateLimit      = "rate_limit_error"
)

// Error codes as defined in the spec.
const (
	ErrCodeInvalidAPIKey           = "invalid_api_key"
	ErrCodeInactiveAPIKey          = "inactive_api_key"
	ErrCodeInactiveAccount         = "inactive_account"
	ErrCodeUnsupportedModel        = "unsupported_model"
	ErrCodeUnsupportedEndpoint     = "unsupported_endpoint"
	ErrCodeUnsupportedFeature      = "unsupported_feature"
	ErrCodeContextLengthExceeded   = "context_length_exceeded"
	ErrCodeProviderUnavailable     = "provider_unavailable"
	ErrCodeProviderRejectedRequest = "provider_rejected_request"
	ErrCodeProviderConfiguration   = "provider_configuration_error"
	ErrCodeProviderRateLimited     = "provider_rate_limited"
	ErrCodeProviderError           = "provider_error"
	ErrCodeProviderTimeout         = "provider_timeout"
	ErrCodeToolSchemaInvalid       = "tool_schema_invalid"
	ErrCodeToolMessageInvalid      = "tool_message_invalid"
	ErrCodeInvalidField            = "invalid_field"
	ErrCodeUnknownField            = "unknown_field"
	ErrCodeInsufficientBalance     = "insufficient_balance"
	ErrCodeDailyTokenLimit         = "daily_token_limit_exceeded"
	ErrCodeConfidentialLimit       = "confidential_request_limit_exceeded"
	ErrCodeGuestNotAllowed         = "guest_not_allowed"
	ErrCodeGuestRequestTooLarge    = "guest_request_too_large"
	ErrCodeClientCanceled          = "client_canceled"
	ErrCodeInternalError           = "internal_error"
)

// GatewayError is the canonical error type for the gateway.
type GatewayError struct {
	HTTPStatus int
	Type       string
	Code       string
	Message    string
	RetryAfter string // Validated provider retry guidance, forwarded as a header.
	// Metadata carries structured diagnostic fields for logging (never serialized to clients).
	Metadata map[string]any
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Type, e.Code, e.Message)
}

// WithMeta returns a shallow copy of the error with additional metadata fields merged in.
func (e *GatewayError) WithMeta(fields ...any) *GatewayError {
	cp := *e
	if cp.Metadata == nil {
		cp.Metadata = make(map[string]any, len(fields)/2)
	} else {
		m := make(map[string]any, len(cp.Metadata)+len(fields)/2)
		for k, v := range cp.Metadata {
			m[k] = v
		}
		cp.Metadata = m
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if key, ok := fields[i].(string); ok {
			cp.Metadata[key] = fields[i+1]
		}
	}
	return &cp
}

// LogFields returns the metadata as a flat slice of key-value pairs for structured logging.
func (e *GatewayError) LogFields() []any {
	if len(e.Metadata) == 0 {
		return nil
	}
	fields := make([]any, 0, len(e.Metadata)*2)
	for k, v := range e.Metadata {
		fields = append(fields, k, v)
	}
	return fields
}

// Error constructors.

func ErrInvalidAPIKey(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 401, Type: ErrTypeAuthentication, Code: ErrCodeInvalidAPIKey, Message: msg}
}

func ErrUnsupportedModel(model string) *GatewayError {
	return &GatewayError{HTTPStatus: 404, Type: ErrTypeInvalidRequest, Code: ErrCodeUnsupportedModel, Message: fmt.Sprintf("Model '%s' is not available", model)}
}

func ErrUnsupportedEndpoint(model, endpoint string) *GatewayError {
	return &GatewayError{HTTPStatus: 422, Type: ErrTypeInvalidRequest, Code: ErrCodeUnsupportedEndpoint, Message: fmt.Sprintf("Model '%s' does not support endpoint '%s'", model, endpoint)}
}

func ErrUnsupportedFeature(feature string) *GatewayError {
	return &GatewayError{HTTPStatus: 422, Type: ErrTypeInvalidRequest, Code: ErrCodeUnsupportedFeature, Message: fmt.Sprintf("Feature '%s' is not supported by the selected model", feature)}
}

func ErrProviderUnavailable(provider string) *GatewayError {
	return &GatewayError{HTTPStatus: 503, Type: ErrTypeProvider, Code: ErrCodeProviderUnavailable, Message: fmt.Sprintf("Provider '%s' is unavailable", provider)}
}

func ErrProviderTimeout(provider string) *GatewayError {
	return &GatewayError{HTTPStatus: 504, Type: ErrTypeProvider, Code: ErrCodeProviderTimeout, Message: fmt.Sprintf("Provider '%s' timed out", provider)}
}

func ErrClientCanceled() *GatewayError {
	return &GatewayError{HTTPStatus: 499, Type: ErrTypeInvalidRequest, Code: ErrCodeClientCanceled, Message: "Client canceled the request"}
}

func ErrInvalidField(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 400, Type: ErrTypeInvalidRequest, Code: ErrCodeInvalidField, Message: msg}
}

func ErrToolMessageInvalid(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 400, Type: ErrTypeInvalidRequest, Code: ErrCodeToolMessageInvalid, Message: msg}
}

func ErrInternal(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 500, Type: ErrTypeInternal, Code: ErrCodeInternalError, Message: msg}
}

func ErrProviderError(status int, msg string) *GatewayError {
	return &GatewayError{HTTPStatus: status, Type: ErrTypeProvider, Code: ErrCodeProviderError, Message: msg}
}

// Control-plane error codes.
const (
	ErrCodeNotFound      = "not_found"
	ErrCodeForbidden     = "forbidden"
	ErrCodeConflict      = "conflict"
	ErrCodeAlreadyExists = "already_exists"
)

func ErrConflict(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 409, Type: ErrTypeInvalidRequest, Code: ErrCodeConflict, Message: msg}
}
func ErrNotFound(resource, id string) *GatewayError {
	return &GatewayError{HTTPStatus: 404, Type: ErrTypeInvalidRequest, Code: ErrCodeNotFound, Message: fmt.Sprintf("%s '%s' not found", resource, id)}
}

// ErrGuestNotAllowed rejects a guest chat request that uses something guests
// cannot: another endpoint or model, a request field, or non-text content.
func ErrGuestNotAllowed(what string) *GatewayError {
	return &GatewayError{HTTPStatus: 403, Type: ErrTypePermission, Code: ErrCodeGuestNotAllowed,
		Message: fmt.Sprintf("Guest chat does not support %s. Sign up to use it.", what)}
}

// ErrGuestRequestTooLarge rejects a guest chat request above the size limit:
// in practice a conversation that grew too long.
func ErrGuestRequestTooLarge(limitBytes int) *GatewayError {
	return &GatewayError{HTTPStatus: 413, Type: ErrTypeInvalidRequest, Code: ErrCodeGuestRequestTooLarge,
		Message: fmt.Sprintf("This conversation is too long for guest chat (limit %d bytes). Start a new chat or sign up to continue.", limitBytes)}
}

func ErrInsufficientBalance() *GatewayError {
	return &GatewayError{HTTPStatus: 402, Type: ErrTypePermission, Code: ErrCodeInsufficientBalance, Message: "Account has no spendable balance"}
}
