package domain

import (
	"errors"
	"unicode/utf8"
)

// Fault names who has to act on an error, so error reports can separate the
// caller's mistakes from provider outages and from Nexus's own failures.
const (
	FaultCaller   = "caller"   // The request or the account must change.
	FaultProvider = "provider" // The upstream provider failed.
	FaultNexus    = "nexus"    // Nexus must fix something: code, config or a provider account.
	FaultUnclear  = "unclear"  // A provider refused the request: a bad request or a Nexus compatibility gap.
)

// Error causes for internal errors. A cause is a fixed value, never text from
// a request or a dependency, so it is always safe to log.
const (
	CausePIIFilterTimeout   = "pii_filter_timeout"
	CausePIIFilterFailed    = "pii_filter_failed"
	CauseProviderKeyMissing = "provider_key_missing"
	CauseCreditHoldLost     = "credit_hold_lost"
	CauseRouterMissing      = "router_not_configured"
	CauseRouterEmptyModel   = "router_empty_model"
)

var faultByCode = map[string]string{
	ErrCodeInvalidAPIKey:           FaultCaller,
	ErrCodeInactiveAPIKey:          FaultCaller,
	ErrCodeInactiveAccount:         FaultCaller,
	ErrCodeUnsupportedModel:        FaultCaller,
	ErrCodeUnsupportedEndpoint:     FaultCaller,
	ErrCodeUnsupportedFeature:      FaultCaller,
	ErrCodeContextLengthExceeded:   FaultCaller,
	ErrCodeToolSchemaInvalid:       FaultCaller,
	ErrCodeToolMessageInvalid:      FaultCaller,
	ErrCodeInvalidField:            FaultCaller,
	ErrCodeUnknownField:            FaultCaller,
	ErrCodeInsufficientBalance:     FaultCaller,
	ErrCodeClientCanceled:          FaultCaller,
	ErrCodeProviderRateLimited:     FaultProvider,
	ErrCodeProviderUnavailable:     FaultProvider,
	ErrCodeProviderTimeout:         FaultProvider,
	ErrCodeProviderError:           FaultProvider,
	ErrCodeProviderRejectedRequest: FaultUnclear,
	ErrCodeProviderConfiguration:   FaultNexus,
	ErrCodeInternalError:           FaultNexus,
}

// FaultOf classifies an error. Errors that are not GatewayErrors reach the
// client as internal errors, so they are Nexus's.
func FaultOf(err error) string {
	var gwErr *GatewayError
	if !errors.As(err, &gwErr) {
		return FaultNexus
	}
	if fault, ok := faultByCode[gwErr.Code]; ok {
		return fault
	}
	return FaultNexus
}

// maxLogValue bounds a logged diagnostic string.
const maxLogValue = 128

// logSafeMeta lists metadata that is safe to log: fixed vocabularies, numeric
// statuses and provider error codes. Messages, reasons and anything that can
// echo request content stay out of logs.
var logSafeMeta = []string{
	"dependency", "cause", "contract",
	"metering_status", "metering_code",
	"router_status", "router_code",
	"upstream_status", "upstream_type", "upstream_code",
	"compatibility_note", "retry_reason", "attempt",
	"account_id", "model", "provider", "requested_model",
}

// DiagnosticFields returns the error's classification and its log-safe
// metadata as key-value pairs, for the log line that reports the error.
func DiagnosticFields(err error) []any {
	fields := []any{"fault", FaultOf(err)}
	var gwErr *GatewayError
	if !errors.As(err, &gwErr) {
		return fields
	}
	fields = append(fields, "error_code", gwErr.Code, "gateway_status", gwErr.HTTPStatus)
	for _, key := range logSafeMeta {
		switch value := gwErr.Metadata[key].(type) {
		case int, int64:
			fields = append(fields, key, value)
		case string:
			if value != "" {
				fields = append(fields, key, truncate(value, maxLogValue))
			}
		}
	}
	return fields
}

// UpstreamStatus returns the provider's HTTP status, or 0 when there was none.
func UpstreamStatus(err error) int {
	var gwErr *GatewayError
	if errors.As(err, &gwErr) {
		if status, ok := gwErr.Metadata["upstream_status"].(int); ok {
			return status
		}
	}
	return 0
}

// Annotate adds metadata to a GatewayError and leaves other errors unchanged.
func Annotate(err error, fields ...any) error {
	var gwErr *GatewayError
	if !errors.As(err, &gwErr) {
		return err
	}
	return gwErr.WithMeta(fields...)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
