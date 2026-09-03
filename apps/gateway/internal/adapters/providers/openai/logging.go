package openai

import (
	"context"
	"sort"

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
	summary := map[string]any{
		"fields": sortedKeys(body),
	}
	copyScalar(summary, body, "model")
	copyScalar(summary, body, "stream")
	copyScalar(summary, body, "max_tokens")
	copyScalar(summary, body, "max_completion_tokens")
	copyScalar(summary, body, "temperature")
	copyScalar(summary, body, "top_p")
	copyScalar(summary, body, "stop")
	copyScalar(summary, body, "presence_penalty")
	copyScalar(summary, body, "frequency_penalty")
	copyScalar(summary, body, "seed")
	copyScalar(summary, body, "logprobs")
	copyScalar(summary, body, "top_logprobs")
	copyScalar(summary, body, "parallel_tool_calls")
	copyScalar(summary, body, "store")
	copyScalar(summary, body, "service_tier")
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
		summary["tool_names"] = summarizeToolNames(tools)
	}
	if toolChoice, ok := body["tool_choice"]; ok {
		summary["tool_choice"] = summarizeToolChoice(toolChoice)
	}
	if responseFormat, ok := body["response_format"].(map[string]any); ok {
		if formatType, ok := responseFormat["type"].(string); ok {
			summary["response_format"] = formatType
		}
	}
	if streamOptions, ok := body["stream_options"].(map[string]any); ok {
		summary["stream_options"] = streamOptions
	}
	return summary
}

func copyScalar(summary, body map[string]any, key string) {
	if v, ok := body[key]; ok {
		summary[key] = v
	}
}

func sortedKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
			item["role"] = role
		}
		if content, ok := msg["content"].(string); ok {
			item["content_chars"] = len(content)
		} else if _, ok := msg["content"]; ok {
			item["content_null"] = true
		}
		if toolCalls, ok := msg["tool_calls"].([]map[string]any); ok {
			item["tool_call_count"] = len(toolCalls)
			item["tool_call_names"] = summarizeToolCallNames(toolCalls)
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

func summarizeToolNames(tools []map[string]any) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func summarizeToolCallNames(toolCalls []map[string]any) []string {
	names := make([]string, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		fn, _ := toolCall["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func summarizeToolChoice(toolChoice any) any {
	switch v := toolChoice.(type) {
	case string:
		return v
	case map[string]any:
		fn, _ := v["function"].(map[string]any)
		name, _ := fn["name"].(string)
		return map[string]any{
			"type":          v["type"],
			"function_name": name,
		}
	default:
		return "[present]"
	}
}
