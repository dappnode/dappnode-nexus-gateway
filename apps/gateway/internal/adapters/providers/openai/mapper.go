package openai

import (
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type providerPolicy struct {
	name                             string
	useMaxTokens                     bool
	explicitNullAssistantToolContent bool
	forwardAssistantReasoningContent bool
	requireToolReasoningContent      bool
	forwardDeveloperRole             bool
}

type builtProviderRequest struct {
	Body       map[string]any
	Policy     string
	Transforms []string
	Omitted    []string
}

type retryBuildResult struct {
	Body        map[string]any
	Omitted     []string
	RetryReason string
	CanRetry    bool
}

func buildRequestBody(req domain.GenerateRequest, model domain.PublicModel) map[string]any {
	return buildProviderRequest(req, model).Body
}

func BuildRequestBody(req domain.GenerateRequest, model domain.PublicModel) map[string]any {
	return buildRequestBody(req, model)
}

func buildProviderRequest(req domain.GenerateRequest, model domain.PublicModel) builtProviderRequest {
	policy := policyForProvider(model.ProviderConfig.ProviderName)
	body := map[string]any{
		"model": model.UpstreamModelName,
	}
	transforms := make([]string, 0, 2)

	messages, messageTransforms := buildMessages(req, policy)
	body["messages"] = messages
	transforms = append(transforms, messageTransforms...)

	if req.MaxOutputTokens != nil {
		v := *req.MaxOutputTokens
		if model.MaxOutputTokens > 0 && v > model.MaxOutputTokens {
			v = model.MaxOutputTokens
		}
		if policy.useMaxTokens {
			body["max_tokens"] = v
			transforms = append(transforms, "token_limit_field=max_tokens")
		} else {
			body["max_completion_tokens"] = v
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort != "" {
		body["reasoning_effort"] = *req.ReasoningEffort
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		if len(req.Stop) == 1 {
			body["stop"] = req.Stop[0]
		} else {
			body["stop"] = req.Stop
		}
	}
	if req.Stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.User != nil {
		body["user"] = *req.User
	}

	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			fn := map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			}
			// Only include "strict" when explicitly true; many providers
			// reject this OpenAI-specific field.
			if t.Strict {
				fn["strict"] = true
			}
			tool := map[string]any{
				"type":     "function",
				"function": fn,
			}
			tools = append(tools, tool)
		}
		body["tools"] = tools
	}

	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case domain.ToolChoiceNone, domain.ToolChoiceAuto, domain.ToolChoiceRequired:
			body["tool_choice"] = req.ToolChoice.Mode
		case domain.ToolChoiceFunction:
			body["tool_choice"] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": *req.ToolChoice.FunctionName,
				},
			}
		}
	}

	if req.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *req.ParallelToolCalls
	}

	// Pass-through parameters
	if req.PresencePenalty != nil {
		body["presence_penalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		body["frequency_penalty"] = *req.FrequencyPenalty
	}
	if len(req.LogitBias) > 0 {
		body["logit_bias"] = req.LogitBias
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if req.Logprobs != nil {
		body["logprobs"] = *req.Logprobs
	}
	if req.TopLogprobs != nil {
		body["top_logprobs"] = *req.TopLogprobs
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if req.ServiceTier != nil {
		body["service_tier"] = *req.ServiceTier
	}

	if req.TextConfig != nil && req.TextConfig.FormatType != nil {
		switch *req.TextConfig.FormatType {
		case "json_object":
			body["response_format"] = map[string]any{"type": "json_object"}
		case "json_schema":
			rf := map[string]any{"type": "json_schema"}
			if req.TextConfig.JSONSchema != nil {
				rf["json_schema"] = req.TextConfig.JSONSchema
			}
			body["response_format"] = rf
		}
	}

	return builtProviderRequest{
		Body:       body,
		Policy:     policy.name,
		Transforms: transforms,
	}
}

func policyForProvider(providerName string) providerPolicy {
	switch providerName {
	case "deepseek":
		// DeepSeek exposes an OpenAI-compatible chat API at /chat/completions,
		// but currently documents the legacy `max_tokens` field and nullable
		// assistant tool-call content.
		return providerPolicy{
			name:                             "deepseek",
			useMaxTokens:                     true,
			explicitNullAssistantToolContent: true,
			forwardAssistantReasoningContent: true,
			requireToolReasoningContent:      true,
		}
	case "novita":
		// Novita exposes an OpenAI-compatible API, but its public Chat
		// Completions docs currently document `max_tokens` rather than
		// OpenAI's newer `max_completion_tokens`, and require a `content`
		// field that may be null for assistant tool-call messages. Keep these
		// as Novita-only wire-shape translations; do not leak them into the
		// public OpenAI-compatible gateway API.
		return providerPolicy{
			name:                             "novita",
			useMaxTokens:                     true,
			explicitNullAssistantToolContent: true,
		}
	default:
		return providerPolicy{
			name:                 "openai-compatible",
			forwardDeveloperRole: providerName == "openai",
		}
	}
}

func buildMessages(req domain.GenerateRequest, policy providerPolicy) ([]map[string]any, []string) {
	var messages []map[string]any
	var transforms []string

	if req.Instructions != nil && *req.Instructions != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": *req.Instructions,
		})
	}

	for _, item := range req.Input {
		msg := map[string]any{}

		role := "user"
		if item.Role != nil {
			role = *item.Role
		}
		if role == "developer" && !policy.forwardDeveloperRole {
			role = "system"
			transforms = append(transforms, "developer_role=system")
		}
		msg["role"] = role

		if item.Content != nil {
			msg["content"] = *item.Content
		} else if policy.explicitNullAssistantToolContent && role == "assistant" && len(item.ToolCalls) > 0 {
			msg["content"] = nil
			transforms = append(transforms, "assistant_tool_content=null")
		}

		if item.ToolCallID != nil {
			msg["tool_call_id"] = *item.ToolCallID
		}

		if role == "assistant" && policy.forwardAssistantReasoningContent {
			if item.ReasoningContent != nil {
				msg["reasoning_content"] = *item.ReasoningContent
			} else if policy.requireToolReasoningContent && len(item.ToolCalls) > 0 {
				msg["reasoning_content"] = ""
				transforms = append(transforms, "assistant_tool_reasoning_content=empty")
			}
		}

		if len(item.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(item.ToolCalls))
			for _, tc := range item.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": tc.ArgumentsJSON,
					},
				})
			}
			msg["tool_calls"] = tcs
		}

		messages = append(messages, msg)
	}

	return messages, transforms
}

func buildNovitaRetryRequest(body map[string]any) retryBuildResult {
	retryBody := cloneBody(body)
	omitted := make([]string, 0, 6)

	for _, field := range []string{"parallel_tool_calls", "store", "service_tier", "user"} {
		if _, ok := retryBody[field]; ok {
			delete(retryBody, field)
			omitted = append(omitted, field)
		}
	}

	if toolChoice, ok := retryBody["tool_choice"]; ok {
		switch v := toolChoice.(type) {
		case string:
			switch v {
			case domain.ToolChoiceAuto:
				delete(retryBody, "tool_choice")
				omitted = append(omitted, "tool_choice=auto")
			case domain.ToolChoiceNone:
				delete(retryBody, "tool_choice")
				delete(retryBody, "tools")
				omitted = append(omitted, "tool_choice=none", "tools")
			}
		}
	}

	return retryBuildResult{
		Body:        retryBody,
		Omitted:     omitted,
		RetryReason: "novita_invalid_request_guarded_downgrade",
		CanRetry:    len(omitted) > 0,
	}
}

func cloneBody(body map[string]any) map[string]any {
	clone := make(map[string]any, len(body))
	for k, v := range body {
		clone[k] = v
	}
	return clone
}
