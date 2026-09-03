package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/google/uuid"
)

// Adapter is the Anthropic provider adapter.
type Adapter struct {
	client *Client
}

func NewAdapter(timeout time.Duration) *Adapter {
	return &Adapter{client: NewClient(timeout)}
}

func (a *Adapter) Generate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	apiKey := resolveAPIKey(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return domain.GenerateResult{}, missingProviderCredentialError()
	}

	body := buildRequestBody(req, model)
	delete(body, "stream")

	respBody, err := a.client.Do(ctx, model.ProviderConfig.BaseURL, apiKey, body)
	if err != nil {
		return domain.GenerateResult{}, mapProviderError(err)
	}

	return parseResponse(respBody, req, model)
}

func (a *Adapter) StreamGenerate(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (ports.GenerationStream, error) {
	apiKey := resolveAPIKey(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return nil, missingProviderCredentialError()
	}

	body := buildRequestBody(req, model)

	resp, err := a.client.DoStream(ctx, model.ProviderConfig.BaseURL, apiKey, body)
	if err != nil {
		return nil, mapProviderError(err)
	}

	return NewStream(resp), nil
}

func missingProviderCredentialError() *domain.GatewayError {
	return domain.ErrInternal("an internal error occurred").WithMeta(
		"provider", "anthropic",
		"reason", "provider API key is not configured",
	)
}

func resolveAPIKey(secretRef string) string {
	return os.Getenv(secretRef)
}

func parseResponse(data json.RawMessage, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type  string `json:"type"`
			Text  string `json:"text,omitempty"`
			ID    string `json:"id,omitempty"`
			Name  string `json:"name,omitempty"`
			Input any    `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return domain.GenerateResult{}, fmt.Errorf("failed to parse provider response: %w", err)
	}

	result := domain.GenerateResult{
		ID:              resp.ID,
		CreatedUnix:     time.Now().Unix(),
		PublicModelID:   req.PublicModelID,
		ProviderName:    model.ProviderConfig.ProviderName,
		ProviderModelID: model.ProviderModelID,
	}

	if result.ID == "" {
		result.ID = uuid.New().String()
	}

	role := resp.Role
	if role == "" {
		role = "assistant"
	}

	out := domain.OutputItem{
		Type: domain.OutputItemTypeMessage,
		Role: &role,
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text := block.Text
			out.Content = &text
		case "tool_use":
			argsJSON, _ := json.Marshal(block.Input)
			out.ToolCalls = append(out.ToolCalls, domain.ToolCall{
				ID:            block.ID,
				Name:          block.Name,
				ArgumentsJSON: string(argsJSON),
			})
		}
	}

	result.Output = []domain.OutputItem{out}

	finishReason := mapStopReason(resp.StopReason)
	result.FinishReason = &finishReason

	// Anthropic input_tokens = only uncached input. Normalize PromptTokens to total input.
	totalInput := resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens
	result.Usage = &domain.Usage{
		PromptTokens:        totalInput,
		CompletionTokens:    resp.Usage.OutputTokens,
		TotalTokens:         totalInput + resp.Usage.OutputTokens,
		CacheCreationTokens: resp.Usage.CacheCreationInputTokens,
		CacheReadTokens:     resp.Usage.CacheReadInputTokens,
	}

	return result, nil
}

func mapProviderError(err error) error {
	if err == nil {
		return nil
	}

	var httpErr *ProviderHTTPError
	if errors.As(err, &httpErr) {
		var gwErr *domain.GatewayError
		switch {
		case httpErr.StatusCode == 401 || httpErr.StatusCode == 403:
			gwErr = domain.ErrProviderError(502, fmt.Sprintf("provider auth/permission error: %s", httpErr.Body))
		case httpErr.StatusCode == 429:
			gwErr = domain.ErrProviderError(429, "provider rate limited: "+httpErr.Body)
		case httpErr.StatusCode == 503:
			gwErr = domain.ErrProviderUnavailable("anthropic")
		case httpErr.StatusCode >= 500:
			gwErr = domain.ErrProviderError(502, fmt.Sprintf("provider server error: %s", httpErr.Body))
		default:
			gwErr = domain.ErrProviderError(502, fmt.Sprintf("provider rejected request: %s", httpErr.Body))
		}
		return gwErr.WithMeta(
			"upstream_status", httpErr.StatusCode,
			"upstream_error", httpErr.Body,
		)
	}

	msg := err.Error()
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
		return domain.ErrProviderTimeout("anthropic").WithMeta("upstream_error", msg)
	}
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") {
		return domain.ErrProviderUnavailable("anthropic").WithMeta("upstream_error", msg)
	}
	return domain.ErrProviderError(502, msg).WithMeta("upstream_error", msg)
}
