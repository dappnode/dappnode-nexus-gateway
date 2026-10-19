package domain

import (
	"errors"
	"strings"
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

// Error causes. A cause is a fixed value, never text from a request or a
// dependency, so it is always safe to log.
const (
	CausePIIFilterTimeout      = "pii_filter_timeout"
	CausePIIFilterFailed       = "pii_filter_failed"
	CauseProviderKeyMissing    = "provider_key_missing"
	CauseProviderNotRegistered = "provider_not_registered"
	CauseCreditHoldLost        = "credit_hold_lost"
	CauseMeteringUnreachable   = "metering_unreachable"
	CauseMeteringErrorStatus   = "metering_error_status"
	CauseMeteringBadResponse   = "metering_bad_response"
	CauseRouterMissing         = "router_not_configured"
	CauseRouterEmptyModel      = "router_empty_model"
	CauseRouterUnreachable     = "router_unreachable"
	CauseRouterErrorStatus     = "router_error_status"
	CauseRouterBadResponse     = "router_bad_response"
	CauseReplayStoreFull       = "replay_store_full"
	CauseUntypedError          = "untyped_error"
	// The provider kept failing, so the gateway stopped sending to it for a while.
	CauseCircuitOpen = "circuit_open"
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
	ErrCodeNotFound:                FaultCaller,
	ErrCodeForbidden:               FaultCaller,
	ErrCodeConflict:                FaultCaller,
	ErrCodeAlreadyExists:           FaultCaller,
	ErrCodeProviderRateLimited:     FaultProvider,
	ErrCodeProviderUnavailable:     FaultProvider,
	ErrCodeProviderTimeout:         FaultProvider,
	ErrCodeProviderError:           FaultProvider,
	ErrCodeProviderRejectedRequest: FaultUnclear,
	ErrCodeProviderConfiguration:   FaultNexus,
	ErrCodeInternalError:           FaultNexus,
}

// FaultOf classifies an error. A "fault" in the metadata wins over the code,
// for codes that name a different party than the one at fault (a
// provider_unavailable raised by a Nexus dependency). Errors that are not
// GatewayErrors reach the client as internal errors, so they are Nexus's.
func FaultOf(err error) string {
	var gwErr *GatewayError
	if !errors.As(err, &gwErr) {
		return FaultNexus
	}
	switch fault, _ := gwErr.Metadata["fault"].(string); fault {
	case FaultCaller, FaultProvider, FaultNexus, FaultUnclear:
		return fault
	}
	if fault, ok := faultByCode[gwErr.Code]; ok {
		return fault
	}
	return FaultNexus
}

// maxLogValue bounds a logged diagnostic string.
const maxLogValue = 128

// RedactedValue replaces an external value that is not identifier-shaped.
const RedactedValue = "[redacted]"

// logSafeMeta lists metadata that is safe to log. Messages, reasons and
// anything that can echo request content stay out of logs.
var logSafeMeta = []struct {
	key string
	// external values come from callers, providers or other services and are
	// logged only when they look like identifiers; the others are set by
	// gateway code.
	external bool
}{
	{"dependency", false}, {"cause", false}, {"contract", false},
	{"metering_status", false}, {"metering_code", true},
	{"router_status", false}, {"router_code", true},
	{"upstream_status", false}, {"upstream_type", true}, {"upstream_code", true},
	{"compatibility_note", false}, {"retry_reason", false}, {"attempt", false},
	{"account_id", true}, {"model", true}, {"provider", true}, {"requested_model", true},
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
	for _, meta := range logSafeMeta {
		switch value := gwErr.Metadata[meta.key].(type) {
		case int, int64:
			fields = append(fields, meta.key, value)
		case string:
			if value == "" {
				continue
			}
			if meta.external && !isIdentifier(value) {
				value = RedactedValue
			}
			fields = append(fields, meta.key, truncate(value, maxLogValue))
		}
	}
	return fields
}

// isIdentifier reports whether s looks like a code, a model ID or an account
// ID rather than free text: short, with no spaces or punctuation beyond
// what identifiers use.
func isIdentifier(s string) bool {
	if len(s) > maxLogValue {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-_./:@+", r):
		default:
			return false
		}
	}
	return true
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
