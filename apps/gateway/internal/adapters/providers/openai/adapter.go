package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/google/uuid"
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

func (a *Adapter) Generate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	apiKey := resolveAPIKey(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return domain.GenerateResult{}, missingProviderCredentialError(model.ProviderConfig.ProviderName)
	}

	built := buildProviderRequest(req, model)
	built.Body["stream"] = false
	delete(built.Body, "stream_options")

	activeBuilt := built
	var rawResp []byte
	var err error
	var retryReason string
	invalidTraceSameBodyRetries := 0
	serverOverloadRetries := 0
	downgradeRetried := false
	for attempt := 1; ; attempt++ {
		a.logProviderRequest(ctx, model, activeBuilt, attempt, retryReason)
		rawResp, err = a.client.Do(ctx, model.ProviderConfig.BaseURL, apiKey, activeBuilt.Body)
		if err == nil {
			break
		}
		if retry := maybeBuildNovitaSameBodyRetry(model, err, activeBuilt.Body); retry.CanRetry {
			if canSpendSameBodyRetry(retry.RetryReason, &invalidTraceSameBodyRetries, &serverOverloadRetries) {
				retryReason = retry.RetryReason
				metrics.ProviderRetries.WithLabelValues(model.ProviderConfig.ProviderName, retryReason).Inc()
				if !sleepBeforeProviderRetry(ctx, retryReason) {
					return domain.GenerateResult{}, withProviderPolicyMeta(mapProviderError(context.Cause(ctx), model.ProviderConfig.ProviderName), activeBuilt, attempt, retryReason)
				}
				continue
			}
		}
		if !downgradeRetried {
			if retry := maybeBuildNovitaDowngradeRetry(model, err, activeBuilt.Body); retry.CanRetry {
				downgradeRetried = true
				retryReason = retry.RetryReason
				metrics.ProviderRetries.WithLabelValues(model.ProviderConfig.ProviderName, retryReason).Inc()
				activeBuilt = builtProviderRequest{
					Body:       retry.Body,
					Policy:     built.Policy,
					Transforms: built.Transforms,
					Omitted:    retry.Omitted,
				}
				if !sleepBeforeProviderRetry(ctx, retryReason) {
					return domain.GenerateResult{}, withProviderPolicyMeta(mapProviderError(context.Cause(ctx), model.ProviderConfig.ProviderName), activeBuilt, attempt, retryReason)
				}
				continue
			}
		}
		return domain.GenerateResult{}, withProviderPolicyMeta(mapProviderErrorWithCompatibilityContext(err, model, activeBuilt.Body), activeBuilt, attempt, retryReason)
	}

	result, err := parseResponse(rawResp, req, model)
	if err != nil {
		return domain.GenerateResult{}, err
	}
	return result, nil
}

func (a *Adapter) StreamGenerate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (ports.GenerationStream, error) {
	apiKey := resolveAPIKey(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return nil, missingProviderCredentialError(model.ProviderConfig.ProviderName)
	}

	built := buildProviderRequest(req, model)

	activeBuilt := built
	var streamResp *http.Response
	var err error
	var retryReason string
	invalidTraceSameBodyRetries := 0
	serverOverloadRetries := 0
	downgradeRetried := false
	for attempt := 1; ; attempt++ {
		a.logProviderRequest(ctx, model, activeBuilt, attempt, retryReason)
		streamResp, err = a.client.DoStream(ctx, model.ProviderConfig.BaseURL, apiKey, activeBuilt.Body)
		if err == nil {
			break
		}
		if retry := maybeBuildNovitaSameBodyRetry(model, err, activeBuilt.Body); retry.CanRetry {
			if canSpendSameBodyRetry(retry.RetryReason, &invalidTraceSameBodyRetries, &serverOverloadRetries) {
				retryReason = retry.RetryReason
				metrics.ProviderRetries.WithLabelValues(model.ProviderConfig.ProviderName, retryReason).Inc()
				if !sleepBeforeProviderRetry(ctx, retryReason) {
					return nil, withProviderPolicyMeta(mapProviderError(context.Cause(ctx), model.ProviderConfig.ProviderName), activeBuilt, attempt, retryReason)
				}
				continue
			}
		}
		if !downgradeRetried {
			if retry := maybeBuildNovitaDowngradeRetry(model, err, activeBuilt.Body); retry.CanRetry {
				downgradeRetried = true
				retryReason = retry.RetryReason
				metrics.ProviderRetries.WithLabelValues(model.ProviderConfig.ProviderName, retryReason).Inc()
				activeBuilt = builtProviderRequest{
					Body:       retry.Body,
					Policy:     built.Policy,
					Transforms: built.Transforms,
					Omitted:    retry.Omitted,
				}
				if !sleepBeforeProviderRetry(ctx, retryReason) {
					return nil, withProviderPolicyMeta(mapProviderError(context.Cause(ctx), model.ProviderConfig.ProviderName), activeBuilt, attempt, retryReason)
				}
				continue
			}
		}
		return nil, withProviderPolicyMeta(mapProviderErrorWithCompatibilityContext(err, model, activeBuilt.Body), activeBuilt, attempt, retryReason)
	}

	return NewStream(streamResp, model.ProviderConfig.ProviderName), nil
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

func parseResponse(data json.RawMessage, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	var resp struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role             string  `json:"role"`
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content,omitempty"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens         int64 `json:"prompt_tokens"`
			CompletionTokens     int64 `json:"completion_tokens"`
			TotalTokens          int64 `json:"total_tokens"`
			PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
			PromptTokensDetails  *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details,omitempty"`
		} `json:"usage"`
		BaseResp *providerBaseResponse `json:"base_resp,omitempty"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return domain.GenerateResult{}, fmt.Errorf("failed to parse provider response: %w", err)
	}
	if err := providerBaseResponseError(resp.BaseResp); err != nil {
		return domain.GenerateResult{}, err
	}

	result := domain.GenerateResult{
		ID:              resp.ID,
		CreatedUnix:     resp.Created,
		PublicModelID:   req.PublicModelID,
		ProviderName:    model.ProviderConfig.ProviderName,
		ProviderModelID: model.ProviderModelID,
	}

	if result.ID == "" {
		result.ID = uuid.New().String()
	}
	if result.CreatedUnix == 0 {
		result.CreatedUnix = time.Now().Unix()
	}

	for _, choice := range resp.Choices {
		role := choice.Message.Role
		out := domain.OutputItem{
			Type:    domain.OutputItemTypeMessage,
			Role:    &role,
			Content: choice.Message.Content,
		}
		if model.ProviderConfig.ProviderName == "deepseek" {
			out.ReasoningContent = choice.Message.ReasoningContent
		}
		for _, tc := range choice.Message.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, domain.ToolCall{
				ID:            tc.ID,
				Name:          tc.Function.Name,
				ArgumentsJSON: tc.Function.Arguments,
			})
		}
		result.Output = append(result.Output, out)
		result.FinishReason = choice.FinishReason
	}

	if resp.Usage != nil {
		result.Usage = &domain.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			CacheReadTokens:  resp.Usage.PromptCacheHitTokens,
		}
		if resp.Usage.PromptTokensDetails != nil && resp.Usage.PromptTokensDetails.CachedTokens > 0 {
			result.Usage.CacheReadTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
	}

	return result, nil
}

func ParseResponse(data []byte, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	return parseResponse(data, req, model)
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

func maybeBuildNovitaDowngradeRetry(model domain.PublicModel, err error, body map[string]any) retryBuildResult {
	if model.ProviderConfig.ProviderName != "novita" || !isInvalidRequestHTTPError(err) {
		return retryBuildResult{}
	}
	return buildNovitaRetryRequest(body)
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
				"compatibility_note", "tool_choice_required_or_named_not_downgraded",
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
		if built.Policy == "novita" && retryReason == "novita_invalid_request_same_body_retry" && len(built.Omitted) == 0 {
			fields = append(fields, "retry_outcome", "same_body_failed_no_safe_downgrade")
		}
	}
	if len(built.Transforms) > 0 {
		fields = append(fields, "transforms", built.Transforms)
	}
	if len(built.Omitted) > 0 {
		fields = append(fields, "omitted_fields", built.Omitted)
	}
	return gwErr.WithMeta(fields...)
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
