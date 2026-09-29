package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Adapter is the OpenAI-compatible provider adapter.
type Adapter struct {
	client *Client
	logger ports.Logger
}

const (
	// Novita sometimes returns a generic 400 invalid_request_error with only a
	// trace_id for otherwise valid tool/chat-history requests, especially on
	// Kimi. Retrying the exact same body preserves proxy semantics and avoids
	// guessing which OpenAI field caused the rejection.
	maxNovitaInvalidTraceSameBodyRetries = 2
	maxNovitaServerOverloadRetries       = 1
)

func NewAdapter(timeout time.Duration, logger ...ports.Logger) *Adapter {
	var l ports.Logger
	if len(logger) > 0 {
		l = logger[0]
	}
	return &Adapter{client: NewClient(timeout), logger: l}
}

func missingProviderCredentialError(providerName string) *domain.GatewayError {
	return domain.ErrInternal("an internal error occurred").WithMeta(
		"provider", providerName,
		"reason", "provider API key is not configured",
	)
}

func resolveAPIKey(secretRef string) string {
	return os.Getenv(secretRef)
}

func mapProviderError(err error, providerName string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return domain.ErrClientCanceled().WithMeta("upstream_error", err.Error())
	}

	// If the upstream provider returned an HTTP error, use its status code.
	var httpErr *ProviderHTTPError
	if errors.As(err, &httpErr) {
		var gwErr *domain.GatewayError
		switch {
		case httpErr.StatusCode == 401 || httpErr.StatusCode == 403:
			// Auth / permission / billing failure at the upstream provider.
			// Novita uses 403 for invalid API key, insufficient balance, and access denied.
			gwErr = domain.ErrProviderError(502, fmt.Sprintf("provider auth/permission error: %s", httpErr.Body))
		case httpErr.StatusCode == 429:
			// Rate limit or token limit exceeded — surface to client so it can back off.
			gwErr = domain.ErrProviderError(429, "provider rate limited: "+httpErr.Body)
		case httpErr.StatusCode == 503:
			// Service unavailable — surface as 503 so clients know to retry.
			gwErr = domain.ErrProviderUnavailable(providerName)
		case httpErr.StatusCode >= 500:
			gwErr = domain.ErrProviderError(502, fmt.Sprintf("provider server error: %s", httpErr.Body))
		default:
			// Client errors from upstream (400, 404, 422, etc.) — the gateway
			// forwarded a request the provider doesn't accept.
			gwErr = domain.ErrProviderError(502, fmt.Sprintf("provider rejected request: %s", httpErr.Body))
		}
		return gwErr.WithMeta(
			"upstream_status", httpErr.StatusCode,
			"upstream_error", httpErr.Body,
			"upstream_type", httpErr.Type,
			"upstream_code", httpErr.Code,
			"upstream_reason", httpErr.Reason,
			"upstream_trace_id", httpErr.TraceID,
		)
	}

	// Network-level errors (no HTTP response received).
	msg := err.Error()
	if strings.Contains(msg, "context canceled") {
		return domain.ErrClientCanceled().WithMeta("upstream_error", msg)
	}
	if contains(msg, "timeout") || contains(msg, "deadline") {
		return domain.ErrProviderTimeout(providerName).WithMeta("upstream_error", msg)
	}
	if contains(msg, "connection refused") || contains(msg, "no such host") {
		return domain.ErrProviderUnavailable(providerName).WithMeta("upstream_error", msg)
	}
	return domain.ErrProviderError(502, msg).WithMeta("upstream_error", msg)
}

func maybeBuildNovitaSameBodyRetry(model domain.PublicModel, err error, body map[string]any) retryBuildResult {
	if model.ProviderConfig.ProviderName != "novita" {
		return retryBuildResult{}
	}
	if isNovitaServerOverload(err) {
		return retryBuildResult{
			Body:        body,
			RetryReason: "novita_server_overload_same_body_retry",
			CanRetry:    true,
		}
	}
	if isGenericNovitaInvalidTraceError(err) && hasToolSurface(body) {
		return retryBuildResult{
			Body:        body,
			RetryReason: "novita_invalid_request_same_body_retry",
			CanRetry:    true,
		}
	}
	return retryBuildResult{}
}

func isInvalidRequestHTTPError(err error) bool {
	var httpErr *ProviderHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		return false
	}
	text := strings.ToLower(strings.Join([]string{httpErr.Body, httpErr.Type, httpErr.Reason}, " "))
	return strings.Contains(text, "invalid")
}

func isGenericNovitaInvalidTraceError(err error) bool {
	var httpErr *ProviderHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		return false
	}
	text := strings.ToLower(httpErr.Body)
	return strings.Contains(text, "invalid request error") && httpErr.TraceID != ""
}

func isNovitaServerOverload(err error) bool {
	var httpErr *ProviderHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		return false
	}
	text := strings.ToLower(strings.Join([]string{httpErr.Body, httpErr.Type, httpErr.Reason}, " "))
	return strings.Contains(text, "server_overload") || strings.Contains(text, "server overload")
}

func canSpendSameBodyRetry(retryReason string, invalidTraceRetries, serverOverloadRetries *int) bool {
	switch retryReason {
	case "novita_invalid_request_same_body_retry":
		if *invalidTraceRetries >= maxNovitaInvalidTraceSameBodyRetries {
			return false
		}
		*invalidTraceRetries++
		return true
	case "novita_server_overload_same_body_retry":
		if *serverOverloadRetries >= maxNovitaServerOverloadRetries {
			return false
		}
		*serverOverloadRetries++
		return true
	default:
		return false
	}
}

func hasToolSurface(body map[string]any) bool {
	if tools, ok := body["tools"]; ok && tools != nil {
		return true
	}
	messages, ok := body["messages"].([]map[string]any)
	if !ok {
		return false
	}
	for _, msg := range messages {
		if role, _ := msg["role"].(string); role == "tool" {
			return true
		}
		if _, ok := msg["tool_calls"]; ok {
			return true
		}
		if _, ok := msg["tool_call_id"]; ok {
			return true
		}
	}
	return false
}

func sleepBeforeProviderRetry(ctx context.Context, retryReason string) bool {
	delay := 150 * time.Millisecond
	if strings.Contains(retryReason, "server_overload") {
		delay = 500 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func mapProviderErrorWithCompatibilityContext(err error, model domain.PublicModel, body map[string]any) error {
	if model.ProviderConfig.ProviderName == "novita" && isInvalidRequestHTTPError(err) && hasNonDroppableToolChoice(body) {
		var httpErr *ProviderHTTPError
		if errors.As(err, &httpErr) {
			return domain.ErrProviderError(502, "provider rejected request; Novita may not support the requested tool_choice semantics for this model: "+httpErr.Body).WithMeta(
				"upstream_status", httpErr.StatusCode,
				"upstream_error", httpErr.Body,
				"upstream_type", httpErr.Type,
				"upstream_code", httpErr.Code,
				"upstream_reason", httpErr.Reason,
				"upstream_trace_id", httpErr.TraceID,
				"compatibility_note", "tool_choice_required_or_named",
			)
		}
	}
	return mapProviderError(err, model.ProviderConfig.ProviderName)
}

func MapProviderErrorWithCompatibilityContext(err error, model domain.PublicModel, body map[string]any) error {
	return mapProviderErrorWithCompatibilityContext(err, model, body)
}

func hasNonDroppableToolChoice(body map[string]any) bool {
	toolChoice, ok := body["tool_choice"]
	if !ok {
		return false
	}
	if mode, ok := toolChoice.(string); ok {
		return mode == domain.ToolChoiceRequired
	}
	_, named := toolChoice.(map[string]any)
	return named
}

func withProviderPolicyMeta(err error, built builtProviderRequest, attempt int, retryReason string) error {
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) {
		return err
	}
	fields := []any{
		"provider_policy", built.Policy,
		"attempt", attempt,
		"provider_params", summarizeProviderBody(built.Body),
	}
	if retryReason != "" {
		fields = append(fields, "retry_reason", retryReason)
		if retryReason == "novita_invalid_request_same_body_retry" {
			fields = append(fields, "retry_outcome", "same_body_failed")
		}
	}
	if len(built.Transforms) > 0 {
		fields = append(fields, "transforms", built.Transforms)
	}
	return gwErr.WithMeta(fields...)
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
