package openai

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func (a *Adapter) logProviderRequest(ctx context.Context, model domain.PublicModel, built builtProviderRequest, attempt int, retryReason string) {
	if a.logger == nil {
		return
	}
	fields := []any{
		"request_id", middleware.GetRequestID(ctx),
		"provider", model.ProviderConfig.ProviderName,
		"provider_model", model.UpstreamModelName,
		"model", model.PublicModelID,
		"provider_policy", built.Policy,
		"attempt", attempt,
		"params", summarizeProviderBody(built.Body),
	}
	if retryReason != "" {
		fields = append(fields, "retry_reason", retryReason)
	}
	if len(built.Transforms) > 0 {
		fields = append(fields, "transforms", built.Transforms)
	}
	if len(built.Omitted) > 0 {
		fields = append(fields, "omitted_fields", built.Omitted)
	}
	a.logger.Info("provider request", fields...)
}

func summarizeProviderBody(body map[string]any) map[string]any {
	// Only fixed keys and numeric/boolean settings: arbitrary strings can contain prompts.
	summary := map[string]any{}
	for _, key := range []string{"stream", "max_tokens", "max_completion_tokens", "temperature", "top_p", "presence_penalty", "frequency_penalty", "seed", "logprobs", "top_logprobs", "parallel_tool_calls", "store"} {
		switch value := body[key].(type) {
		case bool, int, int32, int64, float32, float64:
			summary[key] = value
		}
	}
	if _, ok := body["user"]; ok {
		summary["user"] = "[redacted]"
	}
	if messages, ok := body["messages"].([]map[string]any); ok {
		summary["message_count"] = len(messages)
		summary["messages"] = summarizeMessages(messages)
		summary["total_content_chars"] = totalMessageContentChars(messages)
	}
	if tools, ok := body["tools"].([]map[string]any); ok {
		summary["tool_count"] = len(tools)
	}
	if streamOptions, ok := body["stream_options"].(map[string]any); ok {
		if includeUsage, ok := streamOptions["include_usage"].(bool); ok {
			summary["stream_options"] = map[string]any{"include_usage": includeUsage}
		}
	}
	return summary
}

func summarizeMessages(messages []map[string]any) []map[string]any {
	const maxEdge = 3
	total := len(messages)
	sampled := make([]map[string]any, 0, min(total, maxEdge*2+1))
	for i, msg := range messages {
		if total > maxEdge*2 && i >= maxEdge && i < total-maxEdge {
			if i == maxEdge {
				sampled = append(sampled, map[string]any{"_omitted": total - maxEdge*2})
			}
			continue
		}
		item := map[string]any{}
		if role, ok := msg["role"].(string); ok {
			switch role {
			case "system", "developer", "user", "assistant", "tool", "function":
				item["role"] = role
			default:
				item["role"] = "unknown"
			}
		}
		if content, ok := msg["content"].(string); ok {
			item["content_chars"] = len(content)
		} else if _, ok := msg["content"]; ok {
			item["content_null"] = true
		}
		if toolCalls, ok := msg["tool_calls"].([]map[string]any); ok {
			item["tool_call_count"] = len(toolCalls)
		}
		if reasoningContent, ok := msg["reasoning_content"].(string); ok {
			item["reasoning_content_chars"] = len(reasoningContent)
		} else if _, ok := msg["reasoning_content"]; ok {
			item["has_reasoning_content"] = true
		}
		if _, ok := msg["tool_call_id"]; ok {
			item["has_tool_call_id"] = true
		}
		sampled = append(sampled, item)
	}
	return sampled
}

func totalMessageContentChars(messages []map[string]any) int {
	total := 0
	for _, msg := range messages {
		if content, ok := msg["content"].(string); ok {
			total += len(content)
		}
	}
	return total
}
