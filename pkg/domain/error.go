package domain

import (
	"errors"
	"fmt"
)

// Error types as defined in the spec.
const (
	ErrTypeInvalidRequest = "invalid_request_error"
	ErrTypeAuthentication = "authentication_error"
	ErrTypePermission     = "permission_error"
	ErrTypeProvider       = "provider_error"
	ErrTypeInternal       = "internal_error"
)

// Error codes as defined in the spec.
const (
	ErrCodeInvalidAPIKey         = "invalid_api_key"
	ErrCodeInactiveAPIKey        = "inactive_api_key"
	ErrCodeInactiveAccount       = "inactive_account"
	ErrCodeUnsupportedModel      = "unsupported_model"
	ErrCodeUnsupportedEndpoint   = "unsupported_endpoint"
	ErrCodeUnsupportedFeature    = "unsupported_feature"
	ErrCodeContextLengthExceeded = "context_length_exceeded"
	ErrCodeProviderUnavailable   = "provider_unavailable"
	ErrCodeProviderTimeout       = "provider_timeout"
	ErrCodeToolSchemaInvalid     = "tool_schema_invalid"
	ErrCodeToolMessageInvalid    = "tool_message_invalid"
	ErrCodeInvalidField          = "invalid_field"
	ErrCodeUnknownField          = "unknown_field"
	ErrCodeInsufficientBalance   = "insufficient_balance"
	ErrCodeClientCanceled        = "client_canceled"
	ErrCodeInternalError         = "internal_error"
)

// GatewayError is the canonical error type for the gateway.
type GatewayError struct {
	HTTPStatus int
	Type       string
	Code       string
	Message    string
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

func ErrInactiveAPIKey() *GatewayError {
	return &GatewayError{HTTPStatus: 401, Type: ErrTypeAuthentication, Code: ErrCodeInactiveAPIKey, Message: "API key is inactive"}
}

func ErrInactiveAccount() *GatewayError {
	return &GatewayError{HTTPStatus: 403, Type: ErrTypePermission, Code: ErrCodeInactiveAccount, Message: "Account is inactive"}
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

func ErrInsufficientBalance() *GatewayError {
	return &GatewayError{HTTPStatus: 402, Type: ErrTypePermission, Code: ErrCodeInsufficientBalance, Message: "Account has no spendable balance"}
}

func ErrUnknownField(field string) *GatewayError {
	return &GatewayError{HTTPStatus: 400, Type: ErrTypeInvalidRequest, Code: ErrCodeUnknownField, Message: fmt.Sprintf("Unknown field: '%s'", field)}
}

func ErrToolSchemaInvalid(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 400, Type: ErrTypeInvalidRequest, Code: ErrCodeToolSchemaInvalid, Message: msg}
}

func ErrToolMessageInvalid(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 400, Type: ErrTypeInvalidRequest, Code: ErrCodeToolMessageInvalid, Message: msg}
}

func ErrInternal(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 500, Type: ErrTypeInternal, Code: ErrCodeInternalError, Message: msg}
}

func ErrProviderError(status int, msg string) *GatewayError {
	return &GatewayError{HTTPStatus: status, Type: ErrTypeProvider, Code: ErrCodeProviderUnavailable, Message: msg}
}

// Control-plane error codes.
const (
	ErrCodeNotFound      = "not_found"
	ErrCodeForbidden     = "forbidden"
	ErrCodeConflict      = "conflict"
	ErrCodeAlreadyExists = "already_exists"
)

func ErrNotFound(resource, id string) *GatewayError {
	return &GatewayError{HTTPStatus: 404, Type: ErrTypeInvalidRequest, Code: ErrCodeNotFound, Message: fmt.Sprintf("%s '%s' not found", resource, id)}
}

func ErrForbidden(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 403, Type: ErrTypePermission, Code: ErrCodeForbidden, Message: msg}
}

func ErrConflict(msg string) *GatewayError {
	return &GatewayError{HTTPStatus: 409, Type: ErrTypeInvalidRequest, Code: ErrCodeConflict, Message: msg}
}

func ErrAlreadyExists(resource, id string) *GatewayError {
	return &GatewayError{HTTPStatus: 409, Type: ErrTypeInvalidRequest, Code: ErrCodeAlreadyExists, Message: fmt.Sprintf("%s '%s' already exists", resource, id)}
}

// IsAlreadyExists returns true if the error is a GatewayError with code already_exists.
func IsAlreadyExists(err error) bool {
	var gwErr *GatewayError
	if errors.As(err, &gwErr) {
		return gwErr.Code == ErrCodeAlreadyExists
	}
	return false
}
